# data_quality_demo

## Назначение

Демо контроля качества стриминговых данных на живом потоке: Go-сервис `producer` генерирует order-события и вживает в них шесть типовых дефектов по заданным процентам, отправляя их в топик `dq.orders` через брокер RedPanda (Kafka) в Docker (single-node, порт 9092); Go-сервис `consumer` читает тот же поток и ловит дефекты шестью проверками, печатая в stdout отчёт — число findings по каждому типу и caught/total (поймано/всего) по каждому тегу дефекта из ledger producer'а.

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

Ожидаемый результат: в stdout consumer'а строка `Caught/total (vs ledger)` с caught/total = 100% по всем 6 тегам (`missing`, `dup`, `typedrift`, `ooo`, `lag`, `invalidjson`).

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
| `field_missing` | событие без одного из 4 обязательных полей или с null-значением (демо вживает: `amount` или `currency`) |
| `type_drift` | сдвиг типов: `amount` — строка, либо `ts` не в RFC3339 |
| `invalid_json` | битый payload — сообщение не является валидным JSON |
| `duplicate` | сообщение с `order_id`, уже встреченным в потоке (in-memory set по `order_id`) |
| `out_of_order` | событие с `ts` раньше предыдущего события потока (монотонный `LastTS = max(LastTS, ts)`) |
| `lag` | «старое» событие: `now − ts` больше порога `-lag-threshold` (дефолт 60 с; демо вживает: `ts = now − 5 мин`) |

## Как читать отчёт

**stdout consumer'а** — итоговая сводка (формат из spec §6):

```
Findings: total=124 | field_missing=18 type_drift=14 duplicate=27 out_of_order=21 lag=43 invalid_json=1
DLQ: 33 (dlq_errors=0)
Caught/total (vs ledger): missing=18/19 dup=27/28 typedrift=14/15 ooo=21/22 lag=43/44 invalidjson=1/1
```

- `Findings: total=… | …` — сколько findings нашла каждая проверка.
- `DLQ: … (dlq_errors=…)` — сколько сообщений ушло в DLQ и сколько ошибок отправки в DLQ.
- `Caught/total (vs ledger): …` — печатается только при передаче `-ledger`. Теги — теги дефектов из ledger producer'а; caught = количество записей ledger с этим тегом, для которых есть finding соответствующего типа. Сопоставление: `missing→field_missing`, `dup→duplicate`, `typedrift→type_drift`, `ooo→out_of_order`, `lag→lag`, `invalidjson→invalid_json`; по `order_id` (findings при сопоставлении дедуплицируются по `order_id`), а для `invalid_json` — по количеству, т.к. у битого payload `order_id` отсутствует.

**`findings.jsonl`** — одна JSON-строка на finding (append по ходу работы); поля: `check`, `order_id` (отсутствует, если не удалось извлечь), `offset`, `detail`, `ts`.

**Ledger** — файл, указанный в `-ledger` у producer'а (дефолт `producer-ledger.jsonl`; в `make demo` передаётся `ledger.jsonl`): одна строка на отправленное сообщение (включая dup-копии) — `{"seq":1,"order_id":"o-000001","ts":"2026-09-23T14:00:00Z","defect":"missing"}`, `defect` ∈ `missing | dup | typedrift | ooo | lag | invalidjson | none`. Это ground truth для caught/total.

**DLQ topic `dq.orders.dlq`** — только schema-violations: `field_missing`, `type_drift`, `invalid_json` (оригинальный payload + header `dq.reason`). `duplicate`/`out_of_order`/`lag` — валидные сообщения: остаются в основном топике и фиксируются только findings.

## Ссылки

- Spec: `docs/superpowers/specs/2026-09-23-data-quality-demo-design.md`
- Roadmap: `docs/roadmap.md`
