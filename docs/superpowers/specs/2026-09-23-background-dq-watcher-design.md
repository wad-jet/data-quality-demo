# Фоновый DQ-инспектор (internal/dq) — дизайн-спека

Дата: 2026-09-23
Статус: draft
Продолжение: `docs/superpowers/specs/2026-09-23-data-quality-demo-design.md`
(архитектура producer → RedPanda → consumer), `2026-09-23-audit-tool-design.md` (audit).

## 1. Проблема

1. Проверки качества вшиты в цикл приёма consumer'а
   (`internal/consumer/consumer.go`, `Run`): в одном такте с поллингом
   выполняются проверки, запись в findings.jsonl (файловый I/O) и отправка в DLQ
   (сетевой запрос с Flush). Пока DLQ-отправка идёт, поток стоит: брокер ждёт,
   lag растёт.
2. Producer (`cmd/producer/main.go`) — единый цикл без разделения: генерация,
   отправка, ledger и счётчики перемешаны; на стороне отправки контроля качества
   нет вообще.
3. Слепой автокоммит офсетов: franz-go в group-режиме по умолчанию коммитит
   «всё, что получено» каждые 5 с (`autocommitInterval: 5s` — дефолт franz-go;
   в коде проекта не задан).
   Сообщение может быть помечено прочитанным, хотя проверки ещё не закончили его
   обрабатывать (для фонового воркера это окно ещё шире). При краше такие
   findings теряются **молча**: caught/total ниже, ошибка не видна.
4. Ledger producer'а пишется через `bufio.Writer` (4KB), flush только при
   нормальном `Close`. `kill -9` — хвост буфера теряется.

## 2. Цели / Non-goals

**Цели:**

1. Вынести DQ-проверки в отдельный фоновый встраиваемый компонент
   `internal/dq.Watcher`: точка «ветвления» потока (tap) — очередь; фоновый
   воркер делает проверки, findings, DLQ, агрегацию.
2. Прикрепить компонент к обеим сторонам: consumer (входящий поток) и
   producer (исходящий поток).
3. «Коммит только обработанное»: офсеты consumer'а коммитятся только за
   сообщениями, которые воркер **полностью обработал** (at-least-once без
   потерь: в худшем случае безобидные дубли, потеря — никогда).
4. Ledger: flush после каждой строки (kill -9 безопасность).

**Non-goals:**

- «Блокировка» битых сообщений до отправки (синхронный гейт в момент `Send`) —
  фоновая проверка по определению мониторинг, не охранник.
- Параллельные воркеры / шардирование (нарушил бы порядок, нужный для
  dup/ooo/lag-проверок).
- `fsync` на строку (уровень «отключение света») — вне scope демо.
- Новые флаги consumer'а; изменение формата его отчёта и форматов
  findings.jsonl / ledger.jsonl.

## 3. Архитектура

### 3.1 Компонент `internal/dq.Watcher`

```go
type Config struct {
    Checks     checks.Registry // проверки (чистый пакет internal/checks)
    Findings   FindingsSink    // опционально; nil = не писать findings
    DLQ        DLQSink         // опционально; nil = без DLQ
    BufferSize int             // ёмкость очереди, дефолт 1024
}

type FindingsSink interface { // удовлетворяет *report.FindingsWriter
    Append(checks.Finding) error
    Close() error
}

type DLQSink interface {
    SendToDLQ(ctx context.Context, env checks.Envelope, reason string) error
}

func New(cfg Config) *Watcher
func (w *Watcher) Start()                            // запуск фонового воркера (собственный ctx)
func (w *Watcher) Push(env checks.Envelope)          // блокирует при полном буфере
func (w *Watcher) ProcessedCount() map[int32]int64   // обработано по партициям (конкурентно-безопасно)
func (w *Watcher) Summary() *report.Summary          // валидно только после Close()
func (w *Watcher) Findings() []checks.Finding        // валидно только после Close(); для ComputeCaught
func (w *Watcher) Close() error                      // ограниченный дренаж (таймаут, дефолт 30 с) + close Findings
```

Свойства:

- **Очередь** — ограниченный канал `checks.Envelope`. `Push` при переполнении
  блокирует вызывающего (backpressure). **Никаких дропов** — это гарантия
  полноты findings и caught/total.
- **Один воркер** на Watcher (фоновая горутина, стартует в `Start`). Порядок:
  FIFO → порядок по партиции сохраняется → `checks.State` (map `Seen`,
  `LastTS`) безопасен без блокировок. `Check`-интерфейс и пакет
  `internal/checks` не меняются.
- **Контекст воркера** — собственный (`context.Background()` в `Start`,
  отмена в `Close`), независим от ctx сервиса. При SIGINT дренаж завершается
  полностью, включая DLQ-отправки. **Дренаж ограничен:** `Close()` ждёт не
  дольше таймаута (дефолт 30 с); при превышении — принудительная отмена
  worker-ctx, необработанные envelope логируются и считаются потерянными
  (consumer: их офсеты не коммичены — перечитаются при рестарте).
- **Ошибка DLQ-отправки:** лог + счётчик `dlq_errors`; envelope считается
  обработанным, watermark продвигается (как в текущем дизайне: «лог +
  счётчик, поток продолжается» — зависание shutdown исключено).
- **Контракт потокобезопасности:** `Summary()`/`Findings()` — вызываются
  только после `Close()`, дополнительной синхронизации не требуют
  (`report.Summary` не синхронизирован); `ProcessedCount()` — конкурентно
  безопасен (атомарные обновления).
- **DLQ-решение** — в воркере: для finding'а, где
  `checks.IsSchemaViolation(f.Check)`, вызов `DLQ.SendToDLQ(ctx, env, f.Check)`.
- **`isSchemaViolation`** переезжает из `internal/consumer` (приватный,
  consumer.go:181) в `internal/checks` как экспортный `IsSchemaViolation`.
  Единый источник правды: consumer, `internal/dq` и `internal/audit`
  (текущее «зеркало», audit.go:33) используют одну функцию.
- **Без зависимости от kgo** — компонент встраиваем в любой поток (Kafka
  consumer, Kafka producer, файл, тест).

### 3.2 Consumer (`internal/consumer`)

- Цикл `Run` сокращается до: `PollFetches` → на запись `Push(envelope)` →
  счётчики (`processed`, `lastMsgTime`). Inline-проверки, findings и DLQ из
  цикла убираются; условия остановки (`StopN`, `IdleStop`) и health-check —
  как есть.
- **Офсеты:** клиент создаётся с `kgo.AutoCommitMarks()` — слепой автокоммит
  отключён; в marks-режиме офсеты не «грязнятся» автоматически
  (consumer_group.go: только отметки продвигают head). Периодический автокоммит
  (дефолтный интервал 5 с) коммитит **только отмеченное**; дефолтный
  `OnPartitionsRevoked` тоже коммитит только отмеченное (безопасно: только
  обработанное).
- **Watermark:** основной цикл помнит Pushнутые записи по партициям (порядок
  offsets, указатели `*kgo.Record`). Каждый такт цикла по
  `watcher.ProcessedCount()` помечает `cl.MarkCommitRecords(...)` все записи
   партиции с порядковым номером < обработано (разметка монотонна, kgo не
  допускает rewinds) и освобождает их из буфера. Удержание
  `*kgo.Record`/`Key`/`Value` между поллами безопасно только без
  `kgo.WithPools` (пулы включают refcounted-рециклинг через
  `Record.Recycle` и тихо сломают дизайн); проект пулы не включает.
- **Shutdown:** стоп поллинга → `watcher.Close()` (ограниченный дренаж: до
  дна очереди или таймаута) → пометить остаток → `cl.CommitMarkedOffsets`
  (своим ctx с таймаутом —
  сервисный ctx может быть отменён SIGINT'ом) → `cl.Close()` → summary из
  Watcher → `LoadLedger` → `ComputeCaught` → `Render`.
- **Отчёт** consumer'а — байт-в-байт как сейчас по формату (формат `Render`
  не меняется); findings эквивалентны текущим — `LagCheck` меряет лаг в
  момент обработки воркером, при backpressure значение лага может быть
  чуть выше (незначимо для демо-масштаба).
- Rebalance в демо не ожидается (1 партиция, 1 инстанс); поведение при нём
  корректно по построению (см. выше).

### 3.3 Producer (`cmd/producer`, `internal/producer`)

- Цикл: `ev := gen.Next()` → `emit.Send(...)` → `dq.Push(envelope)` →
  `ledger.Append(...)` → счётчики. **Emitter остаётся чистым** — только
  отправка.
- Envelope producer'а: `Key = []byte(ev.OrderID)`, `Value = ev.Payload`,
  `Partition/Offset = 0` (Kafka назначает их после отправки; проверки не
  используют их для суждений — только для фиксации в finding).
- `Push` **бесусловный** (не зависит от успеха `Send`) — семантика совпадает с
  ledger (текущий код пишет ledger и при ошибке отправки): producer-findings —
  самоконтроль исходящего потока, и он должен сходиться с ledger.
- **Watcher producer'а:** те же 6 проверок (`checks.NewDefault`),
  `FindingsSink` → `producer-findings.jsonl`, `DLQ = nil`. Флаги: `-findings`
  (дефолт `producer-findings.jsonl`), `-lag-threshold` (дефолт `60s` — как у
  consumer'а).
- **Сводка producer'а:** итоговая строка расширяется полем `dq` — число
  findings по каждому типу проверки. caught/total на стороне producer'а
  **не считается** (по построению 100% против собственного ledger —
  информативной ценности нет).
- Shutdown: `dq.Close()` (drain) → `emit.Close()` → `ledger.Close()` →
  сводка.

### 3.4 Ledger (`internal/producer/ledger.go`)

- `Append`: после записи строки — `writer.Flush()`. При `kill -9` ledger не
  теряет последнюю строку. Стоимость для демо (200 строк/с, ~80 Б) —
  пренебрежимо мала.

## 4. Гарантии при крахе (kill -9)

| Данные | При крахе |
|---|---|
| Поток в Kafka | не теряется (producer — доставка с подтверждением `Flush`; consumer ничего не удаляет) |
| Findings consumer'а | **не теряются**: офсет коммитится только после обработки воркером; некомпоченный хвост перечитывается при рестарте → дубли в findings.jsonl (`O_APPEND`) — безобидны, дедуп по `offset` |
| DLQ | сообщения до watermark доставлены (`Flush` в `Emitter.Send`); остаток — перечитка → повторная отправка (дубль безобиден) |
| Ledger producer'а | не теряется (flush на строку, §3.4) |
| producer-findings.jsonl | хвост очереди может быть короче — косметика (ground truth = ledger) |
| Финальный отчёт | не печатается (как и сейчас); пересчитывается audit из findings + ledger |

Семантика: **at-least-once для обработки, never-lose для findings.**

## 5. Что не меняется

- `internal/checks` (сами проверки), `internal/events`, `internal/report`,
  `internal/audit` — без изменений, кроме замены «зеркала» `isSchemaViolation`
  вызовом общей функции (§3.1). Сходимость audit с consumer'ом сохранена
  (findings consumer'а те же).
- Форматы `ledger.jsonl` / `findings.jsonl`; флаги CLI consumer'а; DLQ-топик и
  header `dq.reason`; DoD `make demo` (caught/total = 100% × 6 тегов).

## 6. Тесты

**Unit `internal/dq` (новый пакет, без брокера):**

1. **Drain:** Push N envelope → `Close` → обработано ровно N; findings записаны
   (fake sink / tmp-файл).
2. **Порядок:** детерминированная последовательность с ooo/dup-дефектами →
   ожидаемые findings (проверки видят сообщения в порядке Push).
3. **DLQ:** fake `DLQSink` — вызовы только для schema-violations, reason =
   имя проверки.
4. **Backpressure:** малый буфер (например, 4) + медленный воркер → после
   `Close` обработано всё, ничего не потеряно.
5. **Watermark:** `ProcessedCount` — монотонно неубывающий по партициям.

**Unit `internal/producer`:** после `Append` (до `Close`) строка читаема из
файла (flush на строку).

**Integration (skip без брокера):** consumer-прогон → коммиченный офсет
consumer-группы == число обработанных сообщений (проверка через kgo-клиент,
`OffsetFetch`/групповые офсеты). Тест использует уникальный топик:
коммиченный офсет абсолютный, на фиксированном топике данные накапливаются
между прогонами.

**Существующие** (`checks`, `report`, `audit`, consumer/audit integration) —
без изменений, должны проходить.

**Финальная верификация — `make demo`:** caught/total = 100% × 6 тегов;
`producer-findings.jsonl` создан, число findings совпадает с числом дефектов в
ledger; сводка producer'а содержит `dq`-поле.

## 7. CLI, Makefile, доки

- `cmd/producer`: новые флаги: `-findings` (дефолт `producer-findings.jsonl`),
  `-lag-threshold` (дефолт `60s`). Выключать файлообразование нельзя (YAGNI).
- `Makefile`: цель `clean` удаляет `producer-findings.jsonl`; цель `demo`
  также удаляет его перед запуском (идемпотентные повторные прогоны — файл
  открывается с `O_APPEND`).
- `.gitignore`: + `producer-findings.jsonl`.
- `README.md` + `manual_docs/`: секция «Фоновые проверки и устойчивость к
  краху» (tap+воркер, at-least-once, flush ledger); `manual_docs/reference/` —
  формат `producer-findings.jsonl`.

## 8. Изменения проектного контекста (применяются на шаге 12a)

- `docs/project-context.md` §4/§5: добавить `internal/dq` в модули; обновить
  описания consumer'а (проверки — фоновый воркер) и producer'а (тап в DQ);
  поправить устаревшие ссылки на audit («фаза 2» — сервис уже существует:
  `cmd/audit`, `internal/audit`).

## 9. Риски регрессии

| Модуль | Риск | Сценарий |
|---|---|---|
| `internal/consumer` | MEDIUM — меняется семантика коммита офсетов | `go test ./internal/...` (workdir: `.`) |
| `internal/producer` | LOW — flush в ledger | `go test ./internal/producer/...` (workdir: `.`) |
| `internal/checks` | LOW — перенос `IsSchemaViolation` | `go test ./internal/...` (workdir: `.`) |
| e2e | `make demo` (нужен docker) | `[Manual]` — при недоступности docker фиксируется на гейте 17 |

<!-- maestro:sanitize
status: CLEAN
date: 2026-09-23
hash: 6bfaf870194d5e0b3d1ec0dcd85824dde0a78f9283b120055aecbf3f160d751a
-->

<!-- maestro:review
reviewer: opus
date: 2026-09-23
verdict: approve
hash: e106ae8f612aa212a852e6ac5ed234ecec12fe32696f77680185ec9911a7c6da
-->
