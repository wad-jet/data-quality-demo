# Audit: чтение DLQ-топика + MD/HTML-отчёт — дизайн

Дата: 2026-09-24
Статус: draft
База: `docs/superpowers/specs/2026-09-23-audit-tool-design.md` (audit-тул, §5)

## 1. Назначение

Две фичи audit-тула:

- **Фича A** — audit читает сообщения из DLQ-топика брокера (по флагу) и складывает
  реальные счётчики в `audit-report.json`. Текущая секция `dlq` считается из
  findings.jsonl («ожидаемое»); фича A добавляет «реальное» и сверку между ними —
  расхождение видно как warning (ловит `dlq_errors`, которые сейчас не видны
  ни в одном отчёте).
- **Фича B** — рендер готового `audit-report.json` в человекочитаемый MD и HTML
  (самодостаточный файл для браузера).

## 2. Out of scope

- Примеры/сэмплы DLQ-сообщений в отчёте (только счётчики — решение пользователя).
- Ре-процессинг DLQ (пересылка в основной топик), DLQ-персистентность (volumes
  в docker-compose), новые Makefile-цели besides изменений `demo`.
- Изменения consumer/producer/checks (read-only по их пакетам — как в базе).
- CI, алертинг, Prometheus (как в базе).

## 3. Архитектура (изменения)

| Файл | Изменение |
|---|---|
| `internal/audit/report.go` | `LoadJSON(path) (Report, error)`; `RenderMarkdown() string`; `RenderHTML() string`; `Report.DLQTopic *DLQTopic` (`json:"dlq_topic,omitempty"`); human-строка `DLQ (topic)` |
| `internal/audit/dlq.go` (новый) | `ConsumeDLQ(ctx context.Context, bootstrap, topic string) (DLQTopic, []string, error)` — warnings (нет `dq.reason`, timeout); kmsg-precheck; парсинг заголовка `dq.reason`; наблюдаемое стоп-условие; timeout (именованная константа) |
| `internal/audit/audit.go` | `DLQTopic` struct; функция сверки expected vs actual (warning) |
| `cmd/audit/main.go` | флаги: `-from`, `-format`, `-dlq-topic`, `-bootstrap` |
| `Makefile` | `demo`: audit-шаг до `docker compose down`; `rm -f` + `audit-report.json` |
| `README.md`, `manual_docs/reference` | секция «Аудит»: новые флаги, рендер, поведение demo |

## 4. Фича B — рендер MD/HTML

- Режим: `audit -from audit-report.json -format md|html [-out файл]`.
  - `-from` взаимоисключается с `-ledger`/`-findings` (usage-ошибка, exit 1 —
    единый код со существующими usage-ошибками, main.go:18-22).
  - `-format`: `text` (default, как сейчас human-stdout) | `md` | `html`;
    применим только вместе с `-from` — `md`/`html` без `-from` = usage-ошибка
    (exit 1). Одна точка рендера: build-режим всегда text+JSON-`-out`.
  - `-out` пустой → рендер в stdout; заданный → запись в файл (atomic-паттерн
    `WriteJSON`: tmp+rename).
- `RenderMarkdown()`: заголовок + `generated_at` + inputs; `## Overall`;
  `## Per tag` — markdown-таблица (tag, check, total, caught, recall, findings,
  fp, precision); `## DLQ` — строки expected (offline) и topic (если есть,
  + match/mismatch); `## Timeline` (компактно, как human); `## Warnings`
  (если есть).
- `RenderHTML()`: самодостаточный документ (doctype + `<style>` инлайн, без JS);
  те же данные, per_tag — `<table>`; цветовая пометка mismatch (CSS-класс).
- `LoadJSON`: unmarshal в `Report`; ошибка — путь+строка/поле.
- **Схема JSON (изменение):** `Report.LedgerEntries` — тег
  `json:"ledger_entries,omitempty"` вместо `json:"-"` (audit.go:87): иначе
  после `LoadJSON` human-заголовок «ledger: 0 entries» и round-trip
  `TestLoadJSON` ломаются. Изменение аддитивное (новое поле в
  audit-report.json) — отразить в §9. Рендереры не адаптируются: поле
  сериализуется симметрично.

## 5. Фича A — чтение DLQ-топика

- Режим: `audit -ledger … -findings … -dlq-topic dq.orders.dlq` (флаг пустой —
  офлайн, текущее поведение, брокер не нужен).
- `-bootstrap`: default `DQ_BOOTSTRAP` / `localhost:9092` (как в consumer).
- **Механика consumption** (`ConsumeDLQ(ctx context.Context, bootstrap, topic
  string) (DLQTopic, []string, error)` — второе значение warnings):
  - kgo standalone-консьюм **без consumer group** (deterministic full re-read):
    `kgo.ConsumeTopics(topic)`,
    `kgo.ConsumeResetOffset(kgo.NewOffset().AtStart())` (паттерн
    consumer.go:92; direct-консьюм и так стартует с начала по умолчанию —
    опция явно для самодокументирования),
    `kgo.FetchIsolationLevel(kgo.ReadUncommitted())` (ReadUncommitted —
    дефолт; опция явно для самодокументирования).
  - **Precheck до consume-цикла** (raw kmsg через тот же `*kgo.Client`;
    прецедент — `EnsureTopic`, emit.go:47-74; новая зависимость не нужна —
    kmsg уже в go.mod; kadm в проекте отсутствует):
    1. `kmsg.NewPtrMetadataRequest()` с темой → список партиций. Ошибка
       запроса (брокер недоступен) или код `UNKNOWN_TOPIC_OR_PARTITION`
       (через `kerr.ErrorForCode`, как emit.go:70-73) → `audit: dlq: <err>`
       в stderr, exit 1 — быстрый fail вместо 15-с молчания.
     (End-offset через `ListOffsets` из первоначального черновика НЕ
     используется: на Redpanda v26.2.3 raw `ListOffsetsRequest` всегда
     отвечает `FENCED_LEADER_EPOCH` — проверено фактом 2026-09-24: все
     варианты `Timestamp` (-1/-2/0/конкретный), пустой и непустой топик,
     стабильно. Поэтому стоп-условие — наблюдаемая «тишина», см. ниже.)
  - Счётчики: `count` + `by_reason` по заголовку `dq.reason` (контракт DLQ —
    `internal/consumer/consumer.go:dlqSink`). Нет заголовка → бакет `unknown`
    + warning в возвращаемых warnings.
  - **Стоп-условие (idle-stop):** DLQ-топик статичен (consumer завершает
    дренаж и shutdown до старта audit — новых записей не появляется). Читаем
    записи; если новых записей нет в течение `dlqIdleStop = 2 * time.Second`
    (именованная константа в dlq.go) → стоп. Пустой топик: стоп после одного
    окна тишины, count = 0, без ложного timeout-warning. Каждый `PollFetches`
    вызывается с context, deadline которого = `min(dlqIdleStop, остаток до
    капа)`.
  - **Timeout-кап:** `dlqReadTimeout = 15 * time.Second` (именованная
    константа в dlq.go) — страховочный потолок. Достигнут до «тишины» (в демо
    не происходит) → warning «DLQ-чтение: timeout, возможно неполно» +
    partial-счётчики.
- **Схема отчёта:** `dlq_topic: {"count": N, "by_reason": {...}}`
  (`omitempty`, нет поля — режим офлайн).
- **Сверка:** `dlq_topic` vs `dlq` (expected из findings): count или by_reason
  расходятся → entry в `Warnings` (exit code не меняется — это данные, не
  ошибка, как в базе). Совпадение → human-строка «совпадает с findings».
  Точка слияния: `cmd/audit` — после `BuildReport` вызывает `ConsumeDLQ`,
  присваивает `rep.DLQTopic` и аппендит warnings (unknown-заголовки, timeout,
  свёрку) в `rep.Warnings` до `WriteJSON`/`RenderHuman`.
- **Human-вывод** (после строки `DLQ (offline, schema-violations): …`):
  `DLQ (topic): N (field_missing=a type_drift=b invalid_json=c) — совпадает с findings`
  или `— РАСХОЖДЕНИЕ: ожид. N, факт M (dlq_errors?)`.
  Числа не фиксируются в спеке (см. §8.1) — выводятся фактические значения.
- **Ошибки:** брокер недоступен / топик не найден — определяются в
  kmsg-precheck (до consume-цикла, быстрый fail) → `audit: dlq: <err>` в
  stderr, exit 1 (не паника; без `-dlq-topic` — офлайн, брокер не нужен).

## 6. make demo

- После consumer, **до** `docker compose down`:
  `./bin/audit -ledger ledger.jsonl -findings findings.jsonl -dlq-topic dq.orders.dlq -out audit-report.json`
  (human-отчёт аудита печатается в stdout демо — «полная картина одним вызовом»).
- В блоке `rm -f` в начале демо — добавить `audit-report.json`.
- Порядок безопасен: consumer к выходу завершает дренаж DLQ-эмиттера
  (`consumer.go:shutdown`) — все DLQ-сообщения уже в топике.

## 7. Тесты

- **B unit:** `TestLoadJSON` (round-trip WriteJSON→LoadJSON: равенство полей
  Report, включая `LedgerEntries` — поле сериализуется, см. §4);
  `TestRenderMarkdown` / `TestRenderHTML` (фиксированный Report: ассерты строк
  таблицы, чисел overall/dlq, presence `<table>`/`<h1>` в html).
- **A unit:** парсинг reason (заголовок есть/нет → `unknown`+warning);
  функция сверки (совпадение / mismatch count / mismatch by_reason → warning).
- **A integration** (skip без брокера, паттерн `internal/consumer/integration_test.go`):
  mini producer→consumer (seed 42, 1000) → `ConsumeDLQ` → сверка с эталоном
  этого же прогона: ожидаемые счётчики считаются тестом из findings.jsonl,
  который породил сам тест (той же свёрткой, что `BuildReport` для секции
  `dlq` — фильтр `checks.IsSchemaViolation`, audit.go:283-288), и должны
  совпасть с результатом `ConsumeDLQ` (count и by_reason); плюс инвариант
  `count > 0` (при seed 42 и rate'ах демо schema-нарушения ненулевые).
  Фиксированные константы не захардкожены — findings.jsonl в корне репо
  есть склейка трёх прогонов, числа из него недостоверны.
- **Уникальные топики в integration-тестах A и 13g:** и orders-топик, и
  dlq-топик уникальны на каждый прогон — `dq.it.audit.orders.<unixnano>` и
  `dq.it.audit.orders.<unixnano>.dlq` (образец — uniq-группы,
  integration_test.go:38; текущее фиксированное `dq.it.audit.orders.dlq`
  из integration_test.go:40 не переиспользовать). `ConsumeDLQ` — full
  re-read без группы: фиксированное имя на живом брокере зачитает сообщения
  прошлых прогонов → флаки.
- **Production-путь (13g):** integration-тест audit: producer→consumer→
  `audit.BuildReport` + `ConsumeDLQ` (production-конструкторы, без ручной
  подстановки) → ассерты `DLQTopic` + warnings в Report.
- **cmd:** usage-ошибка на `-from` + `-ledger` одновременно (exit 1 — единый код usage-ошибок, main.go:18-22).

## 8. E2E-критерии приёмки (гейт 17)

1. `make demo` → в `audit-report.json` структурные инварианты:
   `dlq_topic.count == dlq.count`, `dlq_topic.by_reason == dlq.by_reason`,
   `dlq.count > 0`, `warnings` пуст; в stdout демо — строка
   `DLQ (topic) … — совпадает`. Точные числа прогона в спеке не фиксируются:
   baseline чистого `make demo` (один записанный прогон с нуля) фиксируется
   при планировании и используется только как ориентир/дым-проверка,
   не как критерий приёмки.
2. `./bin/audit -from audit-report.json -format md -out audit-report.md` и
   `-format html -out audit-report.html` → файлы созданы, содержат per_tag-таблицу.
3. `audit -dlq-topic` при упавшем брокере → понятная ошибка из kmsg-precheck
   (быстрый fail до consume-цикла, без ожидания 15-с таймаут-капа), exit 1.

## 9. Документация

- README «Аудит»: новые флаги, команда рендера, примечание что `make demo`
  сам пишет `audit-report.json` (с реальным DLQ, т.к. audit идёт до
  broker-down); зафиксировать аддитивное изменение схемы audit-report.json —
  новое поле `ledger_entries` (см. §4) и новое поле `dlq_topic` (omitempty,
  офлайн-режим поле не пишет).
- `manual_docs/reference` — страница audit: синтаксис флагов, форматы отчёта.

<!-- maestro:sanitize
status: CLEAN
date: 2026-09-24
hash: 31eb06fbd946eed4997e6771e464d2ccd5863839624e8c1c4dc61d32b7a059b5
-->

<!-- maestro:review
reviewer: opus
date: 2026-09-24
verdict: approve
hash: 31eb06fbd946eed4997e6771e464d2ccd5863839624e8c1c4dc61d32b7a059b5
-->
