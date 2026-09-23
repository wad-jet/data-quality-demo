# Справочник форматов данных (reference)

Форматы файлов, которые обменивают producer, consumer и audit.
Спецификация поведения: `docs/superpowers/specs/2026-09-23-audit-tool-design.md`.

## ledger.jsonl (producer — ground truth)

Одна JSON-строка на отправленное событие (`producer.LedgerEntry`):

| Поле | Тип | Значение |
|---|---|---|
| `seq` | int64 | порядковый номер отправки (с 1) |
| `order_id` | string | id заказа |
| `ts` | RFC3339 | timestamp события (event-time) |
| `defect` | string | `none` \| `missing` \| `dup` \| `typedrift` \| `ooo` \| `lag` \| `invalidjson` |

Дубль: каждая отправленная копия — отдельная строка с `defect=dup`
(копия повторяет `order_id`, payload и `ts` оригинала).

## findings.jsonl (consumer — detections)

Одна JSON-строка на finding (`checks.Finding`):

| Поле | Тип | Значение |
|---|---|---|
| `check` | string | `field_missing` \| `type_drift` \| `duplicate` \| `out_of_order` \| `lag` \| `invalid_json` |
| `order_id` | string \| absent | пуст у `invalid_json` (order unknown); если пусто — поле не выводится (`omitempty`) |
| `offset` | int64 | offset в топике |
| `detail` | string | описание находки |
| `ts` | RFC3339 | **event-time** (ts заказа, не момент детекции); если в payload нет разпарсиваемого ts — фолбэк на момент детекции |

## audit-report.json (audit)

Схема (JSON, `-out`): `generated_at`, `inputs{ledger,findings}`,
`overall{total_defects,caught,recall,findings,false_positives,precision}`,
`per_tag[{tag,check,total,caught,recall,findings,false_positives,precision,first_ts,last_ts}]`
(порядок: missing, dup, typedrift, ooo, lag, invalidjson),
`dlq{count,by_reason{check:count}}`, `timeline[{bucket_s,count}]` (непустые
по-секундные бакеты по event-time), `warnings[]` (неизвестные значения).

## Метрики

- `recall = caught / total` (0 при total=0)
- `false_positives` — findings check'а, чей `order_id` отсутствует в
  множестве order_id строк ledger данного тега (findings с пустым
  `order_id` тоже считаются ложными); для `invalidjson`:
  `max(0, findings − total)`
- `precision = (findings − false_positives) / findings` (0 при findings=0),
  всегда ≤ 1
- `recall`/`precision` округляются до 4 знаков (round4)

Специфика:
- **Дубли:** `caught` считается по строкам ledger — один finding
  «покрывает» все копии заказа (recall/precision для dup используют
  разные единицы: строки ledger vs findings).
- **`invalid_json`:** `caught = min(findings, total)` (order_id пуст);
  при findings ≤ total precision ≡ 100% (следствие min-правила).
- **Кросс-срабатывание ooo/lag:** «старый ts» срабатывает на обе
  проверки, поэтому precision ooo < 100% (и, в зависимости от
  тайминга, lag) на дет-прогоне — информативный результат, не баг.
- `first_ts`/`last_ts` — min/max `ts` по строкам ledger тега.
