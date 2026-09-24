# Data Quality Demo (Kafka/RedPanda)

Этот проект разработан с помощью [maestro](https://github.com/wad-jet/maestro) — инструмента, который автоматизирует разработку фич с помощью ИИ-агентов (дизайн → спецификация → план → реализация → ревью → мерж).

## Назначение

Демо контроля качества стриминговых данных на живом потоке. Go-сервис `producer` генерирует order-события и вживает в них шесть типовых дефектов по заданным процентам, отправляя их в топик `dq.orders` (канал внутри брокера, куда пишут сообщения) через брокер RedPanda (Kafka-совместимый брокер потоковых сообщений) в Docker (single-node, порт 9092). Go-сервис `consumer` читает тот же поток и ловит дефекты шестью проверками, печатая в stderr (стандартный поток ошибок) через `slog.Info` (стандартный структурированный логгер Go) строку-сводку: число findings (записи о найденных дефектах) по каждому типу и caught/total (поймано/всего) по каждому тегу дефекта из ledger producer'а. Проверки выполняются фоновым воркером на обеих сторонах потока — см. «Фоновые проверки и устойчивость к краху».

## Требования

- Go 1.26.0+ (`go.mod` задаёт `go 1.26.0`)
- Docker с compose-плагином (брокер поднимается по `docker-compose.yml`)

## Быстрый старт

За ~10–15 с вы получите два отчёта: строку producer'а (число findings по каждому типу — поле `dq`) и строку consumer'а, где caught/total = 100% по всем 6 тегам — все вживлённые дефекты пойманы. Формат строк — в «Отчёт и данные».

```
make demo
```

Команда по шагам:

1. `docker compose up -d` — поднимает брокер RedPanda (localhost:9092).
2. Ожидание готовности брокера (`nc -z localhost 9092`, до 60 с).
3. `make build` — сборка `bin/producer`, `bin/consumer` и `bin/audit`.
4. `./bin/producer -count 1000 -rate 200 -seed 42 -ledger out/ledger.jsonl` — 1000 событий, детерминированная последовательность (seed 42), скорость 200 msg/с.
5. `./bin/consumer -stop 1000 -ledger out/ledger.jsonl -findings out/findings.jsonl` — читает 1000 сообщений, печатает отчёт.
6. `docker compose down` — broker останавливается (через trap — в т.ч. по Ctrl-C).

Все производные файлы (ledger, findings, audit-отчёты) складываются в папку `out/` (создаётся автоматически, в gitignore); `make clean` удаляет её целиком.

## Ручной запуск

```
make broker-up   # docker compose up -d
make build       # go build -o bin/producer ./cmd/producer; go build -o bin/consumer ./cmd/consumer; go build -o bin/audit ./cmd/audit
./bin/producer -count 1000 -rate 200 -seed 42 -ledger out/ledger.jsonl
./bin/consumer -stop 1000 -ledger out/ledger.jsonl -findings out/findings.jsonl
make broker-down # docker compose down
```

Полный список флагов — `./bin/producer -h` и `./bin/consumer -h`.

## Проверки (6 тегов)

| Тег | Что ловит |
|---|---|
| `field_missing` | событие без одного из 4 обязательных полей или с null-значением (демо вживает: `amount`) |
| `type_drift` | сдвиг типов: `amount` — строка, либо `ts` не в RFC3339 (формат даты-времени, например `2026-09-23T14:00:00Z`) |
| `invalid_json` | битый payload (тело сообщения, JSON-данные) — сообщение не является валидным JSON |
| `duplicate` | сообщение с `order_id`, уже встреченным в потоке (in-memory set по `order_id`) |
| `out_of_order` | событие с `ts` раньше предыдущего события потока (монотонный `LastTS = max(LastTS, ts)`) |
| `lag` | «старое» событие: `now − ts` больше порога `-lag-threshold` (дефолт 60 с; демо вживает: `ts = now − 5 мин`) |

## Отчёт и данные

**stderr consumer'а** — итоговая сводка: одна строка структурного лога `slog` (дефолтный логгер, уровень INFO, `msg=summary`), сводка целиком в значении поля `report` (формат из spec §6). Пример реального вывода (`make demo`):

```
2026-09-23 20:53:29 INFO summary report="Findings: total=374 | field_missing=42 type_drift=47 duplicate=46 out_of_order=155 lag=61 invalid_json=23\nDLQ: 112 (dlq_errors=0)\nCaught/total (vs ledger): missing=42/42 dup=46/46 typedrift=47/47 ooo=65/65 lag=58/58 invalidjson=23/23\n"
```

(время в начале строки — время прогона; точные числа findings могут незначительно отличаться между прогонами — чувствителен к таймингу `out_of_order` — при стабильном caught/total = 100% по всем тегам)

- `Findings: total=… | …` — сколько findings нашла каждая проверка.
- `DLQ: … (dlq_errors=…)` — сколько сообщений ушло в DLQ и сколько ошибок отправки в DLQ.
- `Caught/total (vs ledger): …` — печатается только при передаче `-ledger`. Теги — теги дефектов из ledger producer'а; caught = количество записей ledger с этим тегом, для которых есть finding соответствующего типа. Сопоставление: `missing→field_missing`, `dup→duplicate`, `typedrift→type_drift`, `ooo→out_of_order`, `lag→lag`, `invalidjson→invalid_json`; по `order_id` (findings при сопоставлении дедуплицируются по `order_id`), а для `invalid_json` — по количеству, т.к. у битого payload `order_id` отсутствует.

**`findings.jsonl`** (дефолт `out/findings.jsonl`) — одна JSON-строка на finding (append по ходу работы); поля: `check`, `order_id` (отсутствует, если не удалось извлечь), `offset`, `detail`, `ts`.

**Ledger** (журнал отправленных сообщений) — файл, указанный в `-ledger` у producer'а (дефолт `out/producer-ledger.jsonl`; в `make demo` передаётся `out/ledger.jsonl`): одна строка на отправленное сообщение (включая dup-копии) — `{"seq":1,"order_id":"o-000001","ts":"2026-09-23T14:00:00Z","defect":"missing"}`, `defect` ∈ `missing | dup | typedrift | ooo | lag | invalidjson | none`. Это ground truth (эталон для сравнения) для caught/total.

**DLQ (dead-letter queue, отдельный топик для сообщений, не прошедших проверку схемы) topic `dq.orders.dlq`** — только schema-violations: `field_missing`, `type_drift`, `invalid_json` (оригинальный payload + header `dq.reason`). `duplicate`/`out_of_order`/`lag` — валидные сообщения: остаются в основном топике и фиксируются только findings.

## Аудит (audit)

Независимая проверка качества по готовым файлам (без брокера), а также —
опционально — чтение реального DLQ-топика (нужен поднятый брокер):

    make build   # собирает и bin/audit
    # Офлайн-режим (без брокера):
    ./bin/audit -ledger out/ledger.jsonl -findings out/findings.jsonl -out out/audit-report.json
    # Онлайн-режим (нужен брокер): дополнительно читает DLQ-топик и сверяет:
    ./bin/audit -ledger out/ledger.jsonl -findings out/findings.jsonl -dlq-topic dq.orders.dlq -out out/audit-report.json
    # Рендер готового отчёта в md/html:
    ./bin/audit -from out/audit-report.json -format md -out out/audit-report.md
    ./bin/audit -from out/audit-report.json -format html -out out/audit-report.html

- В stdout — таблица precision (доля правильных найденных дефектов) / recall (доля найденных дефектов от всех) по 6 дефектам, DLQ по причинам,
  таймлайн, overall; в `audit-report.json` — структурированный отчёт.
- **`make demo` сам прогоняет audit** (до останова брокера) с
  `-dlq-topic dq.orders.dlq` — поэтому `out/audit-report.json` после демо содержит
  реальные счётчики DLQ. В конце демо также рендерит `out/audit-report.md`
  (Markdown-отчёт из того же JSON).
- **Сверка DLQ:** офлайн-секция `dlq` считается из findings («ожидаемое»),
  `dlq_topic` (при `-dlq-topic`) читается из брокера («факт»). Расхождение
  показывается в отчёте как `— РАСХОЖДЕНИЕ` и попадает в `warnings`
  (это данные, а не ошибка: exit 0) — так видны `dlq_errors`, которые не
  фиксируются ни в одном другом отчёте.
- Ожидаемо (дет-режим seed 42, 1000 событий): recall = 100% по всем тегам;
  precision = 100% для missing/typedrift/dup/invalidjson;
  ooo (и, в зависимости от тайминга, lag) — precision < 100%
  (кросс-срабатывание «старого ts» — см. справку);
  `DLQ: 112 (field_missing=42 type_drift=47 invalid_json=23)`, `dlq_errors=0`.
- Форматы отчёта: text (stdout) / JSON (`-out`) / Markdown / HTML
  (`-from … -format md|html`).
- Форматы данных и формулы метрик: `manual_docs/reference/data-formats.md`;
  флаги и режимы audit: `manual_docs/reference/audit.md`.

## Фоновые проверки и устойчивость к краху

Проверки вынесены из циклов отправки/приёма в фоновый воркер
(`internal/dq.Watcher`), прикреплённый к обеим сторонам потока, по двум
причинам: не задерживать приём/отправку (латентность) и не терять findings
при крахе (устойчивость). Ниже — по этим двум осям.

- **consumer** — цикл приёма лишь «тэпает» сообщения — передаёт их в очередь без обработки; проверки,
  findings и DLQ выполняются фоном, не задерживая поллинг.
- **producer** — самоконтроль исходящего потока (те же 6 проверок): findings —
  в `out/producer-findings.jsonl` (флаг `-findings`, дефолт `out/producer-findings.jsonl`);
  `-lag-threshold` — порог проверки lag (дефолт `60s`). Итоговая строка
  producer'а содержит поле `dq` — число findings по каждому типу проверки.
  caught/total на стороне producer'а не считается (ground truth — ledger).

Гарантии:

- **at-least-once / never-lose (гарантия доставки: каждое сообщение будет доставлено минимум один раз, потеря исключена):** офсеты consumer'а (позиции сообщений в топике, «закладка», где уже прочитано) коммитятся только за
  сообщениями, которые воркер полностью обработал («коммит только
  обработанное»). При крахе некоммиченный хвост перечитывается при рестарте —
  дубли в `findings.jsonl` безобидны (дедуп по `offset`), потеря — никогда.
- **Backpressure без дропов (регулирование потока: если буфер заполнен, отправитель ждёт):** при полном буфере воркера цикл отправителя
  ждёт — сообщения не пропускаются.
- **Ledger producer'а:** flush после каждой строки — `kill -9` не теряет
  последнюю запись.
- **Ограниченный дренаж (добор очереди: воркер опустошает её при закрытии):** при остановке очередь добирается до 30 с;
  необработанный хвост перечитывается из Kafka — сервис не виснет.

Подробности и таблица гарантий при крахе:
`docs/superpowers/specs/2026-09-23-background-dq-watcher-design.md` §4.
Формат `producer-findings.jsonl`: `manual_docs/reference/data-formats.md`.

## Ссылки

- Spec: `docs/superpowers/specs/2026-09-23-data-quality-demo-design.md`
- Spec (фоновые проверки): `docs/superpowers/specs/2026-09-23-background-dq-watcher-design.md`
- Roadmap: `docs/roadmap.md`
