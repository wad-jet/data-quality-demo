# Дизайн и spec — data_quality_demo

Дата: 2026-09-23. Статус: утверждён (HITL approve + spec-review opus, правки внесены).
Confidential-контекст: не требовался (проекту нет чувствительных данных; Q/A у custodian не проводился).

## 1. Назначение

Демо на живом потоке (RedPanda): два Go-сервиса показывают, что проверки качества
ловят типовые проблемы стриминговых данных. Producer вживает дефекты по
заданным процентам, consumer ловит их и показывает caught/total по каждой
категории.

## 2. Компоненты

| Компонент | Ответственность |
|---|---|
| `cmd/producer` | генерация order-событий, вживление дефектов, отправка в топик, запись ledger (JSONL) |
| `cmd/consumer` | чтение топика, прогон проверок, DLQ для schema-violations, findings JSONL, агрегат в stdout |
| `internal/events` | схема события, строгий decode/валидация |
| `internal/producer` | генератор (seeded), инжектор дефектов, ledger-writer, kafka-write точка |
| `internal/checks` | интерфейс Check, State, 5 проверок (6 finding-типов) |
| `internal/report` | JSONL-запись findings, агрегация, caught/total по ledger |
| `docker-compose.yml` | RedPanda, один узел, порт 9092 |

Kafka-клиент: **franz-go** (актуально поддерживаемый Go-клиент; при необходимости
заменяется на segmentio/kafka-go). Точки изоляции клиента:
`internal/producer/emit.go` (запись) и `internal/consumer/consumer.go` (чтение).

Топики: `dq.orders` (основной), `dq.orders.dlq` (dead-letter). Consumer group: `dq-demo`.

**Топики создаются явно, 1 партиция, без auto-create.** Producer при старте
создаёт `dq.orders` (franz-go admin; «уже существует» — ignore), consumer —
`dq.orders.dlq`. Одна партиция обязательна для детерминизма `out_of_order`
(проверка опирается на порядок получения).

**Логирование:** `slog`, stdout, структурные логи в обоих сервисах.

## 3. Событие и дефекты

Схема события (JSON), **обязательны все 4 поля**:

```json
{"order_id":"o-000123","amount":199.50,"currency":"USD","ts":"2026-09-23T14:00:00Z"}
```

| Дефект | Вживление (producer) | Детекция (consumer) | Finding |
|---|---|---|---|
| null/missing | `amount` или `currency` пропущены/null | строгая валидация: любое из 4 полей отсутствует/null | `field_missing` |
| сдвиг типов | `amount` — строка; или `ts` не в RFC3339 | `*json.UnmarshalTypeError` | `type_drift` |
| битый payload | producer шлёт не-JSON (`-err-invalidjson`) | `*json.SyntaxError` | `invalid_json` |
| дубликат | **второе сообщение** с уже использованным `order_id`, старым `ts` оригинала | in-memory set по `order_id` | `duplicate` |
| out-of-order | событие с ts раньше предыдущего события потока | `ts < LastTS` (монотонный, см. §4) | `out_of_order` |
| lag | `ts = now − 5 мин` (фикс) | `now − ts > lag_threshold` (дефолт 60с) | `lag` |

В событии вживляется **не более одного** дефекта.

**Ожидаемое мультисрабатывание** (документировано, не баг): dup-копия и
lag-событие несут старый `ts` → дополнительно получают `out_of_order`.
caught/total не искажается: сопоставление идёт по тегу ledger и finding его
типа (таблица в §6).

**DLQ:** только schema-violations (`field_missing`, `type_drift`,
`invalid_json`) → produce в `dq.orders.dlq`: оригинальный payload + header
`dq.reason`. `duplicate`/`out_of_order`/`lag` — валидные сообщения: остаются в
основном потоке, фиксируются только findings. Ошибка отправки в DLQ — лог +
счётчик `dlq_errors` в сводке, поток не останавливается.

## 4. Ядро checks

```go
type Envelope struct {
    Key       []byte
    Value     []byte
    Partition int32
    Offset    int64
}

type Finding struct {
    Check   string    // имя проверки
    OrderID string    // если удалось извлечь
    Offset  int64
    Detail  string
    Ts      time.Time // время события (если извлекается), иначе время получения
}

type Check interface {
    Name() string
    Inspect(ctx context.Context, env Envelope, st *State) []Finding
}

type State struct {
    Seen   map[string]struct{} // order_id → duplicate
    LastTS time.Time           // монотонный: LastTS = max(LastTS, event.ts)
}
```

Порядок прогона: сначала schema-проверки по raw payload (строгий decode);
stateful-проверки — **только для schema-валидных** событий (битые события не
попадают в состояние и не обновляют `LastTS`).

`LastTS` обновляется как `max(LastTS, ts)` (монотонно): старое событие
(lag/dup) не сбрасывает отметку и не вызывает каскад ложных `out_of_order`
для последующих событий. Первое событие (`LastTS.IsZero()`) не даёт
`out_of_order`.

Проверки:
- `schema.go` — `field_missing`, `type_drift`, `invalid_json` (stateless)
- `dup.go` — `duplicate` (set по order_id)
- `order.go` — `out_of_order` (монотонный LastTS потока)
- `lag.go` — `lag` (now − ts события > threshold; broker-timestamp не используется)

Состояние — in-memory, объёмы демо-класса (тысячи событий), eviction не
предусмотрен (YAGNI).

## 5. Producer

Флаги: `-bootstrap` (дефолт: env `DQ_BOOTSTRAP` или `localhost:9092`),
`-topic` (дефолт `dq.orders`), `-count` (0 = непрерывно), `-rate` (msg/с,
дефолт 100), `-err-missing`, `-err-dup`, `-err-typedrift`, `-err-ooo`,
`-err-lag`, `-err-invalidjson` (доли, по умолчанию 0.05, 0.05, 0.05, 0.05,
0.05, 0.02), `-seed` (0 = случайный), `-ledger` (дефолт
`producer-ledger.jsonl`).

**Семантика генератора:**
- на каждое событие одно Bernoulli-испытание по суммарной доле `p = Σ долей`;
- при «дефект да» — тип выбирается по кумулятивным интервалам долей;
- валидация входа: `Σ долей ≤ 1`, иначе exit 2 с сообщением;
- `lag`: фиксированный сдвиг 5 минут назад; `ooo`: фиксированный сдвиг 5 с назад;
- `dup`: берётся случайный `order_id` из уже отправленных, копируется с
  **оригинальным ts** (поэтому dup также даёт `out_of_order` — см. §3);
  при `count` событиях в топик уходит `count + N_dup` сообщений;
- генерация детерминирована seed'ом: один seed → та же последовательность
  событий и дефектов (unit-тесты).

Ledger (JSONL, по строке на отправленное сообщение, включая dup-копии):

```json
{"seq":1,"order_id":"o-000001","ts":"2026-09-23T14:00:00Z","defect":"missing"}
```

`defect` — всегда строка: `missing` | `dup` | `typedrift` | `ooo` | `lag` |
`invalidjson` | `none` (JSON `null` не используется).

**Shutdown (§5.1):** достижение `-count` или SIGINT → завершить генерацию →
drain в полёте (franz-go `Close()` с таймаутом) → flush/закрыть ledger →
сводка в stdout (отправлено, dup-копий, отказов отправки).
Допущение: запись ledger и produce не атомарны (сбой между ними даёт
рассинхрон caught/total) — приемлемо для демо.

## 6. Consumer

Флаги: `-bootstrap` (дефолт: env `DQ_BOOTSTRAP` или `localhost:9092`),
`-topic` (дефолт `dq.orders`), `-group` (дефолт `dq-demo`), `-dlq-topic`
(дефолт `dq.orders.dlq`), `-findings` (дефолт `findings.jsonl`), `-stop`
(обработать N и выйти; 0 = не ограничивать), `-idle-stop` (выйти, если
нет сообщений N таймаута; дефолт выкл) — **`-stop` и `-idle-stop` можно
комбинировать**, `-lag-threshold` (дефолт `60s`), `-ledger` (путь к ledger
producer'а для финальной сводки; не обязателен).

Поведение:
1. Старт: health-check брокера (fetch metadata, таймаут 5с) → при
   недоступности exit 1 с понятным сообщением; создать `dq.orders.dlq`.
2. Цикл: сообщение → checks → findings → JSONL (append, по ходу);
   schema-violation → DLQ.
3. Остановка (`-stop` / `-idle-stop` / SIGINT): flush DLQ-producer,
   закрыть findings-файл → агрегат в stdout:

```
Findings: total=124 | field_missing=18 type_drift=14 duplicate=27 out_of_order=21 lag=43 invalid_json=1
DLQ: 33 (dlq_errors=0)
Caught/total (vs ledger): missing=18/19 dup=27/28 typedrift=14/15 ooo=21/22 lag=43/44 invalidjson=1/1
```

Сопоставление caught/total — по order_id: ledger-запись с тегом дефекта
считается caught, если для её order_id есть finding соответствующего типа.
Явное сопоставление тегов ledger → finding: `missing`→`field_missing`,
`dup`→`duplicate`, `typedrift`→`type_drift`, `ooo`→`out_of_order`,
`lag`→`lag`, `invalidjson`→`invalid_json` (invalid_json — по offset:
order_id у битого payload отсутствует).

## 7. Ошибки

| Ситуация | Поведение |
|---|---|
| Брокер недоступен на старте | health-check → exit 1 + сообщение; integration-тесты `t.Skip()` |
| Poison-сообщение (не JSON) | finding `invalid_json` + DLQ, поток продолжается |
| Ошибка отправки (producer) | ретраи franz-go; счётчик отказов в финальной сводке producer'а |
| Ошибка DLQ-produce (consumer) | лог + счётчик `dlq_errors` в сводке, поток продолжается |

## 8. Тесты

- **unit** (`go test ./...`, без сети):
  - каждая проверка — table-driven по Envelope/State; первый-event guard
    для `out_of_order`; monotonic `LastTS` (старое событие не сбрасывает);
  - строгий decode: valid / missing / type-drift (`UnmarshalTypeError`) /
    invalid JSON (`SyntaxError`) / невалидный ts;
  - генератор: фиксированный seed → точная последовательность событий и
    дефектов; валидация `Σ долей ≤ 1`;
  - агрегация отчёта: caught/total на синтетических ledger/findings.
- **integration** (нужен broker; без него `t.Skip()`):
  - docker-compose → **уникальный consumer group на прогон**
    (`dq-demo-<unix-nano>`) + offset reset `earliest`;
  - producer `-count 1000` (все доли > 0) → consumer `-idle-stop 2s`
    (читает до конца потока, включая dup-копи) → assert: findings > 0 по
    всем 6 типам, DLQ не пуст, caught/total ≥ 95% по каждому тегу.

## 9. Структура файлов (scaffold)

```
go.mod
docker-compose.yml
cmd/producer/main.go
cmd/consumer/main.go
internal/events/order.go        # схема + строгий decode
internal/producer/generator.go  # генерация + инжектор дефектов
internal/producer/ledger.go     # ledger-writer
internal/producer/emit.go       # единственная точка franz-go (запись)
internal/checks/check.go        # Check, Envelope, Finding, State, registry
internal/checks/schema.go
internal/checks/dup.go
internal/checks/order.go
internal/checks/lag.go
internal/consumer/consumer.go   # wiring: read → checks → dlq → report
internal/report/writer.go       # findings JSONL (Finding → JSON-объект, append)
internal/report/summary.go      # агрегация + caught/total
```

## 10. Отклонения от project-context (уточнения дизайна)

- §4/§5: два независимых сервиса (`cmd/producer`, `cmd/consumer`) вместо
  единого сквозного прогона; пакеты — `internal/{events,producer,checks,report}`.
- Отдельный report/audit-сервис для E2E-аудита — **фаза 2** (roadmap); в MVP
  caught/total считает сам consumer по `-ledger`.
