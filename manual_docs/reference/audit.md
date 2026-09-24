# Инструмент audit (reference)

CLI `bin/audit`: строит и рендерит отчёт о качестве потока.
Дизайн: `docs/superpowers/specs/2026-09-24-audit-dlq-report-design.md`.

## Режимы

Два взаимоисключающих режима (одновременное указание — usage-ошибка, exit 1):

- **Build-режим** — из исходных файлов: `-ledger` + `-findings` (оба обязательны).
  Строит отчёт с нуля; всегда печатает text в stdout и (при `-out`) JSON.
- **Render-режим** — из готового отчёта: `-from` (путь к `audit-report.json`).
  Рендерит его в `-format` (text/md/html). `-ledger`/`-findings` вместе с
  `-from` — usage-ошибка; `-dlq-topic`/`-bootstrap` в render-режиме игнорируются.

## Флаги

| Флаг | Режим | Дефолт | Назначение |
|---|---|---|---|
| `-ledger` | build | — (обязателен) | путь к ledger JSONL producer'а |
| `-findings` | build | — (обязателен) | путь к findings JSONL consumer'а |
| `-out` | оба | (пусто) | куда писать: build — JSON-отчёт; render — файл рендера. Пусто — в stdout |
| `-dlq-topic` | build | (пусто) | имя DLQ-топика для чтения; пусто — офлайн (без брокера) |
| `-bootstrap` | build | `$DQ_BOOTSTRAP` или `localhost:9092` | адрес брокера для `-dlq-topic` |
| `-from` | render | — | путь к `audit-report.json` для рендера |
| `-format` | render | `text` | формат рендера: `text` (human) \| `md` \| `html` |

## Форматы отчёта

- **text** — human-таблица в stdout (дефолт build-режима и `-format text`).
- **JSON** — `audit-report.json` (build + `-out`). Схема — в
  `manual_docs/reference/data-formats.md` (§ audit-report.json).
- **Markdown** — `-from … -format md`: заголовок, overall, таблица per_tag,
  DLQ (offline + topic, если есть), таймлайн, warnings.
- **HTML** — `-from … -format html`: самодостаточный файл (инлайн CSS, без JS);
  расхождение DLQ подсвечивается CSS-классом `mismatch`.

## Exit codes

- `0` — успех, **включая** расхождение DLQ (это данные, а не ошибка).
- `1` — usage-ошибки (неверные/конфликтующие флаги) и ошибки выполнения,
  в т.ч. при `-dlq-topic`: брокер недоступен или топик не найден — быстрая
  ошибка из precheck (`audit: dlq: …`), без ожидания таймаута.

## DLQ-топик режим (`-dlq-topic`)

Если флаг задан, после построения отчёта audit читает **все** сообщения
DLQ-топика (полный re-read без consumer group) и кладёт фактические счётчики
в поле `dlq_topic` отчёта.

- **Предпочтительно с поднятым брокером** — `make demo` прогоняет audit до
  останова брокера именно для этого. Если брокер упал — ошибка с exit 1.
- DLQ — статичный топик (consumer завершил дренаж до старта audit), поэтому
  чтение останавливается после 2 с «тишины» (нет новых сообщений), общий
  потолок — 15 с.
- **Сверка:** `dlq` (офлайн, из findings — «ожидаемое») против `dlq_topic`
  («факт»). Расхождение → строка `— РАСХОЖДЕНИЕ` в отчёте и запись в
  `warnings[]` (exit 0). Ловит `dlq_errors`, невидимые в других отчётах.

## Пример вывода (build-режим, с `-dlq-topic`)

    Audit report (ledger: 1000 entries, findings: 374)
    tag        check              total   caught    recall   findings   fp  precision
    missing    field_missing         42       42    100.0%         42    0     100.0%
    dup        duplicate             46       46    100.0%         46    0     100.0%
    typedrift  type_drift            47       47    100.0%         47    0     100.0%
    ooo        out_of_order          65       65    100.0%        155   88      43.2%
    lag        lag                   58       58    100.0%         61    0     100.0%
    invalidjson invalid_json          23       23    100.0%         23    0     100.0%
    DLQ (offline, schema-violations): 112 (field_missing=42 type_drift=47 invalid_json=23)
    DLQ (topic): 112 (field_missing=42 type_drift=47 invalid_json=23) — совпадает с findings
    Overall: recall=1.0000 precision=0.7647 (caught=281/281, findings=374, fp=88)
