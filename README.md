# data_quality_demo

## Назначение

Демо контроля качества стриминговых данных на живом потоке: Go-сервис `producer` генерирует order-события и вживает в них шесть типовых дефектов по заданным процентам, отправляя их в топик `dq.orders` через брокер RedPanda (Kafka) в Docker (single-node, порт 9092); Go-сервис `consumer` читает тот же поток и ловит дефекты шестью проверками, печатая в stderr (через `slog.Info` с дефолтным логгером) строку-сводку — число findings по каждому типу и caught/total (поймано/всего) по каждому тегу дефекта из ledger producer'а.

## Требования

- Go 1.26.0+ (`go.mod` задаёт `go 1.26.0`)
- Docker с compose-плагином (брокер поднимается по `docker-compose.yml`)

## Quickstart

```
make demo
```

Команда по шагам:

1. `docker compose up -d` — поднимает брокер RedPanda (localhost:9092).
2. Ожидание готовности брокера (`nc -z localhost 9092`, до 60 с).
3. `make build` — сборка `bin/producer` и `bin/consumer`.
4. `./bin/producer -count 1000 -rate 200 -seed 42 -ledger ledger.jsonl` — 1000 событий, детерминированная последовательность (seed 42), скорость 200 msg/с.
5. `./bin/consumer -stop 1000 -ledger ledger.jsonl -findings findings.jsonl` — читает 1000 сообщений, печатает отчёт.
6. `docker compose down` — broker останавливается (через trap — в т.ч. по Ctrl-C).

Занимает ~10–15 с (DoD плана: < 60 с).

Ожидаемый результат: в stderr consumer'а строка структурного лога `slog` (уровень INFO, `msg=summary`, сводка в поле `report`), где в `Caught/total (vs ledger)` caught/total = 100% по всем 6 тегам (`missing`, `dup`, `typedrift`, `ooo`, `lag`, `invalidjson`).

## Ручной запуск

```
make broker-up   # docker compose up -d
make build       # go build -o bin/producer ./cmd/producer; go build -o bin/consumer ./cmd/consumer
./bin/producer -count 1000 -rate 200 -seed 42 -ledger ledger.jsonl
./bin/consumer -stop 1000 -ledger ledger.jsonl -findings findings.jsonl
make broker-down # docker compose down
```

Полный список флагов — `./bin/producer -h` и `./bin/consumer -h`.

## Проверки (6 тегов)

| Тег | Что ловит |
|---|---|
| `field_missing` | событие без одного из 4 обязательных полей или с null-значением (демо вживает: `amount`) |
| `type_drift` | сдвиг типов: `amount` — строка, либо `ts` не в RFC3339 |
| `invalid_json` | битый payload — сообщение не является валидным JSON |
| `duplicate` | сообщение с `order_id`, уже встреченным в потоке (in-memory set по `order_id`) |
| `out_of_order` | событие с `ts` раньше предыдущего события потока (монотонный `LastTS = max(LastTS, ts)`) |
| `lag` | «старое» событие: `now − ts` больше порога `-lag-threshold` (дефолт 60 с; демо вживает: `ts = now − 5 мин`) |

## Как читать отчёт

**stderr consumer'а** — итоговая сводка: одна строка структурного лога `slog` (дефолтный логгер, уровень INFO, `msg=summary`), сводка целиком в значении поля `report` (формат из spec §6). Пример реального вывода (`make demo`):

```
2026-09-23 20:53:29 INFO summary report="Findings: total=374 | field_missing=42 type_drift=47 duplicate=46 out_of_order=155 lag=61 invalid_json=23\nDLQ: 112 (dlq_errors=0)\nCaught/total (vs ledger): missing=42/42 dup=46/46 typedrift=47/47 ooo=65/65 lag=58/58 invalidjson=23/23\n"
```

(время в начале строки — время прогона; точные числа findings могут незначительно отличаться между прогонами — чувствителен к таймингу `out_of_order` — при стабильном caught/total = 100% по всем тегам)

- `Findings: total=… | …` — сколько findings нашла каждая проверка.
- `DLQ: … (dlq_errors=…)` — сколько сообщений ушло в DLQ и сколько ошибок отправки в DLQ.
- `Caught/total (vs ledger): …` — печатается только при передаче `-ledger`. Теги — теги дефектов из ledger producer'а; caught = количество записей ledger с этим тегом, для которых есть finding соответствующего типа. Сопоставление: `missing→field_missing`, `dup→duplicate`, `typedrift→type_drift`, `ooo→out_of_order`, `lag→lag`, `invalidjson→invalid_json`; по `order_id` (findings при сопоставлении дедуплицируются по `order_id`), а для `invalid_json` — по количеству, т.к. у битого payload `order_id` отсутствует.

**`findings.jsonl`** — одна JSON-строка на finding (append по ходу работы); поля: `check`, `order_id` (отсутствует, если не удалось извлечь), `offset`, `detail`, `ts`.

**Ledger** — файл, указанный в `-ledger` у producer'а (дефолт `producer-ledger.jsonl`; в `make demo` передаётся `ledger.jsonl`): одна строка на отправленное сообщение (включая dup-копии) — `{"seq":1,"order_id":"o-000001","ts":"2026-09-23T14:00:00Z","defect":"missing"}`, `defect` ∈ `missing | dup | typedrift | ooo | lag | invalidjson | none`. Это ground truth для caught/total.

**DLQ topic `dq.orders.dlq`** — только schema-violations: `field_missing`, `type_drift`, `invalid_json` (оригинальный payload + header `dq.reason`). `duplicate`/`out_of_order`/`lag` — валидные сообщения: остаются в основном топике и фиксируются только findings.

## Аудит (audit)

Независимая проверка качества по готовым файлам (без брокера):

    make build   # собирает и bin/audit
    ./bin/audit -ledger ledger.jsonl -findings findings.jsonl -out audit-report.json

- В stdout — таблица precision/recall по 6 дефектам, DLQ по причинам,
  таймлайн, overall; в `audit-report.json` — структурированный отчёт.
- Ожидаемо (дет-режим seed 42): recall = 100% по всем тегам;
  precision = 100% для missing/typedrift/dup/invalidjson;
  ooo (и, в зависимости от тайминга, lag) — precision < 100%
  (кросс-срабатывание «старого ts» — см. справку).
- Форматы данных и формулы метрик: `manual_docs/reference/data-formats.md`.

## Ссылки

- Spec: `docs/superpowers/specs/2026-09-23-data-quality-demo-design.md`
- Roadmap: `docs/roadmap.md`
