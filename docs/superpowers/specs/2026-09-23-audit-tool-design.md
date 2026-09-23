# Spec: cmd/audit — E2E-аудит качества (Фаза 2)

Дата: 2026-09-23 · Ветка: feature/audit-tool · Roadmap: docs/roadmap.md, Фаза 2 (P1)
Исходный spec: docs/superpowers/specs/2026-09-23-data-quality-demo-design.md

## 1. Назначение

Независимый инструмент верификации качества: по готовым файлам
(`ledger.jsonl` + `findings.jsonl`) **offline** (без брокера) считает
precision/recall по каждому из 6 дефектов, DLQ-состав по причинам,
таймлайн находок. Человекочитаемый отчёт в stdout, структурированный —
JSON-файл по флагу `-out`.

Цель: показать ценность независимого E2E-аудита — audit пересчитывает
метрики по тому же ground truth (ledger producer'а), что и агрегат
consumer'а, и обязан сходиться с ним на тех же данных.

## 2. Non-goals

- Нет доступа к брокеру/DLQ topic (DLQ — offline-вывод, §5).
- Нет алертинга, порогов выхода по метрикам (exit code — только ошибки),
  конфигов, Prometheus, web.
- Нет изменений в consumer/producer/checks (read-only по их пакетам:
  переиспользуются типы и Summary).

## 3. Архитектура

```
cmd/audit/main.go        — CLI: флаги, запуск, вывод (stdout/JSON), exit codes
internal/audit/audit.go  — загрузка ledger+findings, расчёт per-tag метрик
internal/audit/report.go — структура отчёта (human + JSON), рендер
internal/audit/*_test.go — unit-тесты на синтетических данных
manual_docs/reference/data-formats.md — справочная статья (ledger/findings/audit)
```

Зависимости: `dqdemo/internal/checks` (тип `Finding`, имена проверок),
`dqdemo/internal/producer` (тип `LedgerEntry`), `dqdemo/internal/report`
(`Summary`: `LoadLedger` + `ComputeCaught` — переиспользуются как есть).

## 4. Метрики per дефект (ключевое решение)

Ground truth = ledger producer'а (`defect` per строка); detections =
findings consumer'а. **caught и total — переиспользуют `report.Summary`**
(`LoadLedger` → `TagTotal`/`tagOrders`, `ComputeCaught(findings)` →
`Caught`), чтобы audit гарантированно сходился с агрегатом consumer
(DoD). precision/false positives считает audit дополнительно:

Для каждого тегa (маппинг тег ledger → check: `missing→field_missing`,
`dup→duplicate`, `typedrift→type_drift`, `ooo→out_of_order`,
`lag→lag`, `invalidjson→invalid_json`):

- `total` = `TagTotal[tag]` (строки ledger с этим дефектом; для dup —
  **каждая отправленная копия отдельной строкой**, сырой счёт)
- `caught` = `Caught[tag]` из `ComputeCaught`
- `findings` = `ByCheck[check]` (всего findings этого типа)
- `false_positives` = число findings этого check'а, чей `order_id`
  **отсутствует** в множестве order_id строк ledger данного тега
  (для `invalidjson`, где order_id пуст: `max(0, findings − total)`)
- `recall` = `caught / total` (0 если total = 0)
- `precision` = `(findings − false_positives) / findings` (0 если
  findings = 0); тождественно ≤ 1

**Асимметрия dup (обязательно в отчёте и доках):** `ComputeCaught`
считает caught **по строкам ledger**: `tagOrders` — slice с дублями,
каждая строка, чей order_id есть в уникальном наборе order_id
findings, даёт +1. Один finding «покрывает» все копии заказа:
ledger: order A дубль ×3 (3 строки defect=dup); findings: 1 finding
`duplicate` с order_id=A → total=3, **caught=3**, recall=100%,
findings=1, fp=0, precision=100%. caught может превышать findings —
это ledger-перспектива полноты, не ошибка. Для dup recall и precision
используют разные единицы сопоставления (строки ledger vs findings) —
явно указывается в доках.

**invalid_json** (order_id пуст): caught = `min(findings, total)` —
как в `ComputeCaught`; precision/fp формулы общие.

**Кросс-срабатывание ooo/lag (ожидание, не баг):** дефекты `ooo` и
`lag` несут общий симптом «старый ts», поэтому event с запланованным
`lag` может дать finding `out_of_order` и наоборот (фактический
дет-прогон: findings ooo=155 при total ledger ooo=65; lag=61 при 58).
Поэтому precision для ooo/lag < 1 — информативный результат аудита:
fp здесь — findings с order_id, помеченным в ledger другим дефектом.
Числа findings ooo/lag тайминг-зависимы и могут слегка дрейфовать
между прогонами (caught/total стабильны).

Округление: recall/precision — 4 знака после запятой в JSON; в
human-таблице — процент с 1 знаком.

## 5. DLQ-секция (offline-вывод)

Правило consumer'а: в DLQ уходят только schema-violations — findings
проверок `field_missing`, `type_drift`, `invalid_json`. Audit выводит:
`dlq.count` (сумма) + `dlq.by_reason` (счётчики per check). Чтение
DLQ topic с брокера — не требуется и не поддерживается.

## 6. Таймлайн

`timeline` — по-секундные бакеты по всем findings (bucket = unix-секунда
`ts` finding'а), компактный список `{bucket_s, count}`, только
непустые бакеты. **Важно: это event-time, не detection-time** —
`Finding.Ts` = ts заказа (события), поэтому lag-findings попадают в
бакеты ~5 минут «в прошлом» относительно времени прогона.
`first_ts`/`last_ts` per tag в `per_tag` — min/max `ts` по
**ledger-строкам данного тега** (per_tag — ledger-концепт).
Полный дамп событий — нет.

## 7. CLI и вывод

```
dqdemo-audit -ledger ledger.jsonl -findings findings.jsonl [-out audit-report.json]
```

- `-ledger`, `-findings` — обязательны; отсутствие/непарсебельная строка
  → ошибка в stderr, exit 1.
- `-out` — путь JSON (пусто = не писать). Запись JSON — atomically
  (tmp + rename); ошибка записи → stderr, exit 1.
- Человекочитаемый отчёт — **stdout** (Unix-утилита, редиректится);
  числа примера иллюстративные (фактические — в DoD §10):

```
Audit report (ledger: 1000 entries, findings: 374)
tag        check           total  caught  recall   findings  fp  precision
missing    field_missing      42      42   100.0%        42    0   100.0%
dup        duplicate          46      46   100.0%        46    0   100.0%
...
ooo        out_of_order       65      65   100.0%       155   90    41.9%
lag        lag                58      58   100.0%        61    3    95.1%
...
DLQ (offline, schema-violations): 112 (field_missing=42 type_drift=47 invalid_json=23)
Timeline (1s buckets): t=1758700000:12 t=1758700001:25 ...
Overall: recall=1.0000 precision=0.7513 (caught=281/281, findings=374, fp=93)
```

- JSON-схема `-out`:

```json
{
  "generated_at": "RFC3339",
  "inputs": {"ledger": "ledger.jsonl", "findings": "findings.jsonl"},
  "overall": {"total_defects": 281, "caught": 281, "recall": 1.0,
               "findings": 374, "false_positives": 93, "precision": 0.7513},
  "per_tag": [{"tag": "missing", "check": "field_missing", "total": 42,
               "caught": 42, "recall": 1.0, "findings": 42,
               "false_positives": 0, "precision": 1.0,
               "first_ts": "RFC3339", "last_ts": "RFC3339"}],
  "dlq": {"count": 112, "by_reason": {"field_missing": 42,
          "type_drift": 47, "invalid_json": 23}},
  "timeline": [{"bucket_s": 1758700000, "count": 12}]
}
```

- Exit codes: 0 — успех (даже при fp>0 или recall<1 — это данные, не
  ошибка); 1 — ошибки входа/IO/parse.
- Неизвестные значения (defect вне 6 тегов в ledger / check вне 6 имён
  в findings) — warning в stderr + исключение из per_tag (не ошибка).
- `overall` — суммы/агрегаты по 6 тегам: total_defects = Σ total,
  caught = Σ caught, findings = Σ findings, false_positives = Σ fp,
  recall = Σcaught/Σtotal, precision = (Σfindings − Σfp)/Σfindings
  (0 при знаменателе 0).
- Порядок элементов `per_tag` — `tagOrder` (missing, dup, typedrift,
  ooo, lag, invalidjson).
- Сборка: `make build` дополняется `go build -o bin/audit ./cmd/audit`.

## 8. Тестирование

1. **Unit (синтетика, без сети)** в `internal/audit`:
   - per-tag метрики на сконструированных ledger+findings: идеал
     (100%/100%), fp (finding с order_id, отсутствующим в
     ledger-наборе тега → fp=1, precision<1; вкл. кросс-кейс:
     ledger tag=lag order A + finding out_of_order(A) →
     caught[lag]=1, fp[ooo]=1), пропуск (defect в ledger без
     finding),
     асимметрия dup из §4 (A×3 + 1 finding → total=3, caught=3,
     recall=1.0, findings=1, fp=0, precision=1.0), invalid_json
     min-правило, пустые файлы, total=0.
   - DLQ-группировка (3 причины, не-schema-violation не в DLQ).
   - Timeline-бакеты (findings в разных секундах).
   - JSON-схема: marshal → ожидаемые поля; `-out` запись (tmp dir).
2. **Сходимость с consumer (integration, skip без брокера)**:
   мини-прогон producer→consumer (паттерн
   `internal/consumer/integration_test.go`), затем audit на его
   ledger+findings; ассерт: `caught/total` audit == `Caught/TagTotal`
   consumer на всех 6 тегах. Пометка: сходимость по построению
   квазитаутологична (общий `Summary`) — ценность теста: round-trip
   через файлы; поэтому плюс sanity-ассерт: при непустых тегах
   total/caught/findings > 0.

## 9. Документация (manual_docs)

`manual_docs/reference/data-formats.md` — справочная статья (Diátaxis:
Reference): форматы `ledger.jsonl` (LedgerEntry: seq/order_id/ts/defect,
значения defect), `findings.jsonl` (Finding: check/order_id/offset/
detail/ts, имена check), формат JSON audit-отчёта (§7), формулы метрик
с асимметрией dup и разными единицами recall/precision (§4), кэверза:
precision(invalid_json) ≡ 100% при findings ≤ total (следствие
min-правила), таймлайн — event-time (§6). Обновить README.md: секция
про `audit` (команда, флаги, где читать отчёт).

## 10. DoD

- `go build ./...`, `go vet ./...`, `go test ./...` зелёные; unit-тесты
  §8.1 покрывают все формулы из §4 (вкл. dup-асимметрию, invalid_json,
  кросс-срабатывание ooo/lag).
- Integration-сходимость (§8.2) PASS при поднятом брокере.
- Живой прогон (дет-режим, seed 42): `make demo` →
  `./bin/audit -ledger ledger.jsonl -findings findings.jsonl
  -out audit-report.json` (бинарник — из `make build`) →
  recall = 100% по всем 6 тегам; precision = 100% для
  missing/typedrift/dup/invalidjson; ooo/lag — fp > 0 (кросс-
  срабатывание §4), значения детерминированные в пределах прогона;
  JSON валиден; отчёт в stdout; README/manual_docs обновлены.

## 11. Риски и пометки

- Дубли: см. §4 — caught считается по строкам ledger (один finding
  покрывает все копии), recall и precision — разные единицы;
  обязательно в доках, иначе метрика читается как баг.
- Переиспользование `report.Summary` тянет audit в зависимости от
  consumer-ориентированного пакета — осознанный выбор ради гарантии
  сходимости (DoD); при будущем изменении семантики caught меняется
  и audit (это корректно — ground truth один).
- `out_of_order`/`lag` — тайминг-зависимые: числа findings могут
  слегка дрейфовать между прогонами (покрыто в README фазы 1);
   на дет-прогоне: recall = 100%; precision ooo/lag < 100%
   (fp > 0, §4).

<!-- maestro:sanitize
status: CLEAN
date: 2026-09-23
hash: 69e1e3282df7b4f529d94922f498c60bdf4cbc1446255b74985d73fc9a6a345d
-->

<!-- maestro:review
reviewer: opus
date: 2026-09-23
verdict: approve
hash: 69e1e3282df7b4f529d94922f498c60bdf4cbc1446255b74985d73fc9a6a345d
-->
