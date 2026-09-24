# Plan: audit — чтение DLQ-топика + MD/HTML-отчёт

Дата: 2026-09-24
Spec: `docs/superpowers/specs/2026-09-24-audit-dlq-report-design.md` (approved, verdict: approve)
Ветка: `feature/audit-dlq-report`

## Базовые значения чистого прогона (орентир, НЕ критерий приёмки — spec §8.1)

Записано 2026-09-24 одним чистым `make demo` (seed 42, 1000 событий) + `audit`:
- producer: `sent=1000 defects=281`, `dq="field_missing=42 type_drift=47 duplicate=46 out_of_order=155 lag=61 invalid_json=23"`
- consumer: `Findings: total=374`, **`DLQ: 112 (dlq_errors=0)`**, caught/total = 100% по всем тегам
- audit: ledger=1000, findings=374; `DLQ (offline, schema-violations): 112 (field_missing=42 type_drift=47 invalid_json=23)`;
  overall: recall=1.0000, precision=0.7647 (fp=88 — кросс-срабатывания ooo)

## Global constraints (из спеки)

- TDD: тесты пишутся до реализации; commit per task (после зелёных тестов).
- Tесты: table-driven unit; integration — skip без брокера на `localhost:9092`
  (паттерн `internal/audit/integration_test.go` / `internal/consumer/integration_test.go`).
- Атомарная запись файлов — tmp+rename (как `WriteJSON`, report.go:54).
- slog для логов; exit codes: 0 — успех (включая mismatch — это данные),
  1 — usage-ошибки и ошибки выполнения (единый код, main.go:18-22).
- Spec follow-up (не блокирует): формат ошибки `LoadJSON` — путь + строка/поле
  через `json.Decoder` + `InputOffset` (spec §4, Minor round 1).
- Не менять consumer/producer/checks (spec §2, read-only по их пакетам).

## Task 1 — B-core: LoadJSON + RenderMarkdown + RenderHTML (tier: sonnet)

**Spec:** §4, §7 (B unit).
**Files:**
- Modify: `internal/audit/audit.go` (тег `LedgerEntries` → `json:"ledger_entries,omitempty"`, spec §4)
- Modify: `internal/audit/report.go` (`LoadJSON`, `RenderMarkdown()`, `RenderHTML()`)
- Create: `internal/audit/render_test.go`

**Steps (TDD):**
1. RED: `render_test.go`:
   - `TestLoadJSON` — round-trip WriteJSON→LoadJSON: равенство полей Report,
     включая `LedgerEntries` (spec §7: «включая LedgerEntries»).
   - `TestRenderMarkdown` — фиксированный Report (пер_tag 2 строки, dlq,
     warnings 1 шт): ассерты — `#`-заголовок, строки таблицы `| missing |`
     / `| ooo |`, числа overall (recall/precision), строка DLQ, `## Warnings`.
   - `TestRenderHTML` — тот же Report: presence `<!doctype`, `<h1>`, `<table>`,
     `<th>recall</th>`, чисел overall, CSS-класса mismatch (spec §4).
2. `go test ./internal/audit/ -run 'TestLoadJSON|TestRender' -v` → fail.
3. GREEN: реализовать `LoadJSON` (unmarshal; ошибка — путь + строка/поле,
   см. spec follow-up), `RenderMarkdown`, `RenderHTML` (самодостаточный
   документ, инлайн CSS, без JS); поменять тег `LedgerEntries`.
4. `go test ./internal/audit/` → ok. `go build ./...` → ok.
5. Commit: `feat(audit): LoadJSON + MD/HTML renderers + ledger_entries field`

## Task 2 — B-cli: режим -from/-format (tier: haiku)

**Spec:** §4 (режим рендера, usage-ошибки), §7 (cmd).
**Depends:** Task 1.
**Files:**
- Modify: `cmd/audit/main.go`
- Create: `cmd/audit/main_test.go`

**Steps (TDD):**
1. RED: `main_test.go` (вынести обработку флагов в тест-функцию
   `runAudit(args []string, stdout, stderr io.Writer) int` — в main.go):
   - `-from` + `-ledger` одновременно → exit 1 (spec §4: единый код usage).
   - `-format md` без `-from` → exit 1 (spec §4).
   - `-from <tmp json> -format md` (без `-out`) → md в stdout, exit 0.
   - `-from <tmp json> -out f.md` → файл создан, stdout пуст.
   - tmp-JSON — WriteJSON фиксированного Report (round-trip).
2. `go test ./cmd/audit/ -v` → fail.
3. GREEN: флаги `-from`, `-format` (default `text`); взаимоисключения;
   рендер в stdout/файл.
4. `go test ./cmd/audit/` → ok; `go build ./...` → ok.
5. Commit: `feat(audit): -from/-format render mode`

## Task 3 — A-core: ConsumeDLQ + Report.DLQTopic (tier: sonnet)

**Spec:** §3, §5 (механика, сверка, human-вывод, ошибки), §7 (A unit).
**Files:**
- Create: `internal/audit/dlq.go`
- Create: `internal/audit/dlq_test.go`
- Modify: `internal/audit/audit.go` (`Report.DLQTopic *DLQTopic`, `json:"dlq_topic,omitempty"`)
- Modify: `internal/audit/report.go` (human-строка `DLQ (topic)` в `RenderHuman`)

**Steps (TDD):**
1. RED: `dlq_test.go` (unit, без брокера):
   - `parseDLQReason` (или эквивалент): заголовок `dq.reason` есть → reason;
     нет → `unknown` + warning в возвращаемый список (spec §5).
   - `checkDLQTopic(expected DLQ, actual DLQTopic) string`: совпадение → "";
     mismatch count → warning; mismatch by_reason → warning (spec §5 «Сверка»).
   - `RenderHuman`: Report с `DLQTopic` → строка `DLQ (topic): N (…) — совпадает
     с findings`; без совпадения → `— РАСХОЖДЕНИЕ` (spec §5 «Human-вывод»).
2. `go test ./internal/audit/ -run 'TestParseDLQReason|TestCheckDLQTopic|TestRenderHuman' -v` → fail.
3. GREEN: `dlq.go`:
   - `type DLQTopic struct { Count int; ByReason map[string]int }`
   - `const dlqReadTimeout = 15 * time.Second`
   - `func ConsumeDLQ(ctx context.Context, bootstrap, topic string) (DLQTopic, []string, error)`:
     - precheck raw kmsg (прецедент `EnsureTopic`, emit.go:47-74):
       `kmsg.NewPtrMetadataRequest()` → партиции (ошибка запроса или
       `UNKNOWN_TOPIC_OR_PARTITION` через `kerr.ErrorForCode` → error);
       `kmsg.NewPtrListOffsetsRequest()` `Timestamp: -1` → end-offsets.
     - kgo client: `ConsumeTopics(topic)`,
       `ConsumeResetOffset(kgo.NewOffset().AtStart())`,
       `FetchIsolationLevel(ReadUncommitted())`; цикл PollFetches;
       счётчики по заголовку; стоп: для всех p `lastOffset+1 >= end[p]`;
       timeout-кап `dlqReadTimeout` → warning + partial.
   - `checkDLQTopic` + human-строка + поле `Report.DLQTopic`.
4. `go test ./internal/audit/` → ok; `go build ./...` → ok.
   (ConsumeDLQ e2e проверяется в Task 4 integration.)
5. Commit: `feat(audit): ConsumeDLQ — kmsg precheck + group-less consume`

## Task 4 — A-cli + integration: флаги, wiring, production-путь (tier: sonnet)

**Spec:** §5 (точка слияния, ошибки), §7 (A integration, уникальные топики, 13g), §8.3.
**Depends:** Task 3.
**Files:**
- Modify: `cmd/audit/main.go` (флаги `-dlq-topic` default "", `-bootstrap`
  default env `DQ_BOOTSTRAP`/`localhost:9092`; после `BuildReport`: если
  `-dlq-topic` != "" → `ConsumeDLQ`, `rep.DLQTopic = ...`, append warnings
  (включая сверку) в `rep.Warnings` до WriteJSON/RenderHuman; ошибка
  ConsumeDLQ → `audit: dlq: <err>`, exit 1)
- Create: `internal/audit/dlq_integration_test.go`

**Steps (TDD):**
1. RED: `dlq_integration_test.go` (skip без брокера):
   - уникальные топики: `dq.it.audit.orders.<unixnano>` и
     `…<unixnano>.dlq` (spec §7: фиксированное имя не использовать);
     uniq-группы по образцу integration_test.go:38.
   - mini producer→consumer (seed 42, 1000, те же параметры, что
     `internal/audit/integration_test.go`).
   - expected: тест считает сам из findings.jsonl, созданных этим прогоном,
     фильтром `checks.IsSchemaViolation` (та же свёртка, audit.go:283-288).
   - `ConsumeDLQ(ctx, bootstrap, dlqTopic)` → assert: count == expected.count,
     by_reason равны, `count > 0`, warnings пуст.
   - **13g production-путь:** production-конструкторы без ручной подстановки
     (реальный Emitter/kgo-клиент, как в integration_test.go).
   - негатив: `ConsumeDLQ` на несуществующем топике → error (precheck),
     не timeout-ожидание.
2. `go test ./internal/audit/ -run Integration -v` (с поднятым брокером —
   оркестратор поднимает `make broker-up` перед прогоном) → fail (нет флагов?
   нет — ConsumeDLQ есть после Task 3; тест падает только если реализация
   неверна). cmd-часть: wiring-тест через `runAudit` (main_test.go из Task 2):
   `-dlq-topic` на упавшем брокере → exit 1 + stderr `audit: dlq:` (без
   брокера — skip-вариант, либо явный под-тест с dead-port).
3. GREEN: флаги + wiring в main.go.
4. `go test ./...` → ok (integration — skip без брокера).
5. Commit: `feat(audit): -dlq-topic flag + DLQ-topic integration test`

## Task 5 — Makefile: audit в make demo (tier: haiku)

**Spec:** §6.
**Depends:** Task 4.
**Files:** Modify: `Makefile`

**Steps:**
1. В target `demo` после consumer-строки, до EXIT-trap'а cleanup:
   `echo "[demo] running audit (-dlq-topic dq.orders.dlq -out audit-report.json)...";`
   `./bin/audit -ledger ledger.jsonl -findings findings.jsonl -dlq-topic dq.orders.dlq -out audit-report.json;`
2. В `rm -f` (строка 21-22) добавить `audit-report.json`.
3. Smoke (оркестратор): `make demo` → в stdout секция аудита со строкой
   `DLQ (topic) … — совпадает`; `audit-report.json` содержит `dlq_topic`.
4. Commit: `feat(demo): audit with live DLQ before broker-down`

## Task 6 — Docs: README + manual_docs (tier: haiku)

**Spec:** §9.
**Depends:** Task 5.
**Files:**
- Modify: `README.md` (секция «Аудит»)
- Create: `manual_docs/reference/audit.md` (стиль — как
  `manual_docs/reference/data-formats.md`)

**Steps:**
1. README «Аудит»: новые флаги (`-dlq-topic`, `-bootstrap`, `-from`,
   `-format`), команда рендера (md/html), примечание что `make demo` сам
   пишет `audit-report.json` (с реальным DLQ — audit до broker-down);
   аддитивное изменение схемы audit-report.json: поля `ledger_entries`,
   `dlq_topic` (omitempty); ориентир чистого прогона: DLQ=112
   (42/47/23), dlq_errors=0.
2. `manual_docs/reference/audit.md`: синтаксис всех флагов, режимы
   (build/render), форматы отчёта (text/md/html/JSON), exit codes,
   DLQ-topic-режим (предпочтительно: брокер жив), пример вывода.
3. Commit: `docs: audit — DLQ-topic + MD/HTML render`

## Spec Coverage Matrix

| Требование спеки | Task |
|---|---|
| §4: `LoadJSON`, `RenderMarkdown`, `RenderHTML` | T1 |
| §4: `LedgerEntries` → `ledger_entries,omitempty` (схема) | T1 (+ T6 docs) |
| §4: `-from`/`-format`, usage-ошибки exit 1, stdout/`-out` | T2 |
| §5: `ConsumeDLQ` — precheck (Metadata+ListOffsets), consume, стоп, timeout, warnings | T3 |
| §5: `DLQTopic`, `Report.DLQTopic`, сверка + warning | T3 |
| §5: human-строка `DLQ (topic)` | T3 |
| §5: точка слияния warnings в `cmd/audit`, exit 1 на ошибки precheck | T4 |
| §5: `-dlq-topic`/`-bootstrap` флаги | T4 |
| §7: B unit (round-trip, MD/HTML ассерты) | T1 |
| §7: cmd usage-тесты | T2 |
| §7: A unit (reason, сверка, human) | T3 |
| §7: A integration (эталон из собственного findings, count>0) | T4 |
| §7: уникальные топики в integration | T4 |
| §7: 13g production-путь | T4 |
| §6: make demo — audit до broker-down, `rm -f` | T5 |
| §8.1: E2E-инварианты (structural) | шаг 15/17 (оркестратор, smoke T5) |
| §8.2: рендер md/html из audit-report.json | шаг 17 (ручной, оркестратор) |
| §8.3: ошибка на упавшем брокере, exit 1 | T4 (негативный под-тест) + шаг 17 |
| §9: README + manual_docs | T6 |
| Follow-up: формат ошибки LoadJSON (json.Decoder/Offset) | T1 |

## Regression risk (для entry, шаг 12a)

Сигналы: cross-layer (CLI → internal/audit → брокер) → **MEDIUM**.
- Модули риска: `internal/audit` (новый consumption-путь), `Makefile` (демо-флоу).
- Сценарии (для entry):
  - S1 `path: cmd/audit/main.go` — офлайн-режим не сломан:
    `run: ./bin/audit -ledger ledger.jsonl -findings findings.jsonl` (exit 0,
    строка `DLQ (offline`), `workdir: .`
  - S2 `path: Makefile` — демо с живым DLQ:
    `run: make demo` (exit 0, в stdout `DLQ (topic)` и `— совпадает`),
    `workdir: .`
  - S3 `path: internal/audit/dlq.go` — live-DLQ интеграция:
    `run: make broker-up && go test ./internal/audit/ -run Integration -count=1`
    (broker поднимать/ронять вокруг), `workdir: .`
