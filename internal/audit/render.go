package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"dqdemo/internal/checks"
)

// LoadJSON reads a JSON report from the given path.
func LoadJSON(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		switch e := err.(type) {
		case *json.SyntaxError:
			// compute line and column from offset
			line := 1
			col := int(e.Offset)
			// Iterate bytes to compute line and column
			for i, b := range data {
				if i+1 >= int(e.Offset) {
					break
				}
				if b == '\n' {
					line++
					col = int(e.Offset) - i - 1
				}
			}
			return Report{}, fmt.Errorf("%s:%d:%d: %w", path, line, col, err)
		case *json.UnmarshalTypeError:
			if e.Field != "" {
				return Report{}, fmt.Errorf("%s: field %s: %w", path, e.Field, err)
			}
			return Report{}, fmt.Errorf("%s: %w", path, err)
		default:
			return Report{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return r, nil
}

// RenderMarkdown — русский «человеческий» MD-отчёт (spec §4).
func RenderMarkdown(r Report) (string, error) {
	var b strings.Builder
	b.WriteString("# Отчёт о качестве данных (audit)\n\n")
	healthy, problems := Verdict(r)
	if healthy {
		b.WriteString("**Вердикт: проблем не обнаружено.**\n\n")
		if r.DLQTopic != nil {
			b.WriteString("Все заложенные дефекты найдены; сверка DLQ — совпадает.\n\n")
		} else {
			b.WriteString("Все заложенные дефекты найдены.\n\n")
		}
	} else {
		fmt.Fprintf(&b, "**Вердикт: обнаружены проблемы (%d).**\n\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Что проверяли\n\n")
	k := r.Overall.Findings - r.Overall.FalsePos - r.Overall.Caught
	if r.Overall.Findings == 0 {
		fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Записей инспектора (consumer — читает каждое событие и фиксирует нарушения) нет.\n\n",
			r.LedgerEntries, r.Overall.TotalDefects)
	} else {
		caughtNote := "(найдены все)"
		if r.Overall.Caught < r.Overall.TotalDefects {
			caughtNote = fmt.Sprintf("(найдено %d из %d)", r.Overall.Caught, r.Overall.TotalDefects)
		}
		fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Инспектор (consumer — читает каждое событие и фиксирует нарушения) — записей всего %d:\n",
			r.LedgerEntries, r.Overall.TotalDefects, r.Overall.Findings)
		fmt.Fprintf(&b, "- %d — заложенные дефекты %s,\n", r.Overall.Caught, caughtNote)
		if k > 0 {
			fmt.Fprintf(&b, "- %d — дополнительные записи на те же дефекты (повторные отправки),\n", k)
		}
		fmt.Fprintf(&b, "- %d — ложные срабатывания (не подтвердились при сверке с ledger).\n", r.Overall.FalsePos)
		fmt.Fprintf(&b, "\nПодтвердились записи: %d из %d — это и есть Precision.\n\n",
			r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings)
	}

	b.WriteString("## Метрики простыми словами\n\n")
	b.WriteString("| Метрика | Значение | Что это значит |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Recall | %d/%d (%s) | Доля заложенных дефектов, которые удалось найти |\n",
		r.Overall.Caught, r.Overall.TotalDefects, Pct(r.Overall.Recall))
	fmt.Fprintf(&b, "| Precision | %d/%d (%s) | Доля находок, которые подтвердились; остальные — ложные срабатывания |\n",
		r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings, Pct(r.Overall.Precision))
	b.WriteString("\n")

	b.WriteString("## Дефекты по видам\n\n")
	b.WriteString("| Дефект | Что это | Заложено | Найдено из заложенных | Recall | Всего записей | Ложных | Precision |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, tm := range r.PerTag {
		recall, prec := "—", "—"
		if tm.Total > 0 {
			recall = Pct(tm.Recall)
		}
		if tm.Findings > 0 {
			prec = Pct(tm.Precision)
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %d | %d | %s |\n",
			tm.Tag, TagDescription(tm.Tag), tm.Total, tm.Caught, recall, tm.Findings, tm.FalsePos, prec)
	}
	b.WriteString("\n")
	for _, tm := range r.PerTag {
		if (tm.Tag == "ooo" || tm.Tag == "lag") && tm.FalsePos > 0 {
			b.WriteString("Событие с «старой» отметкой времени (lag) приходит и не по порядку — поэтому инспектор фиксирует его дважды: как lag и как ooo. Запись ooo не совпадает ни с одним заложенным ooo-дефектом и считается для ooo «ложной», хотя порядок действительно был нарушен. Это ожидаемое поведение демо, а не ошибка детектора.\n\n")
			break
		}
	}
	b.WriteString("**Как читать колонки:**\n\n")
	b.WriteString("- **Заложено** — сколько дефектов этого вида producer вживил намеренно (записи ledger).\n")
	b.WriteString("- **Найдено из заложенных** — сколько из них инспектор нашёл (Заложено = Найдено → Recall 100%).\n")
	b.WriteString("- **Всего записей** — все записи инспектора этого вида. Запись — на сообщение, а «заложено/найдено» — на дефект: один дефект может дать несколько записей (повторная отправка сообщения).\n")
	b.WriteString("- **Ложных** — записи, не подтвердившиеся при сверке с ledger.\n\n")

	b.WriteString("## DLQ (dead-letter queue) — очередь проблемных сообщений\n\n")
	if r.DLQ.Count > 0 {
		var parts []string
		for _, tag := range tagOrder {
			check := tagToCheck[tag]
			if checks.IsSchemaViolation(check) {
				if n := r.DLQ.ByReason[check]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d (%s)", n, tag))
				}
			}
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "Должно быть: %d = %s (по находкам инспектора, без обращения к брокеру)\n",
				r.DLQ.Count, strings.Join(parts, " + "))
		} else {
			fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, без обращения к брокеру)\n", r.DLQ.Count)
		}
	} else {
		fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, без обращения к брокеру)\n", r.DLQ.Count)
	}
	if r.DLQTopic != nil {
		fmt.Fprintf(&b, "Фактически: %d (прочитано из брокера)\n", r.DLQTopic.Count)
		if m := CheckDLQTopic(r.DLQ, *r.DLQTopic); m == "" {
			b.WriteString("Статус: совпадает\n")
		} else {
			fmt.Fprintf(&b, "Статус: РАСХОЖДЕНИЕ — %s\n", strings.TrimPrefix(m, "DLQ-сверка: "))
		}
	} else {
		b.WriteString("Фактически: не считалось (требуется запущенный брокер и флаг `-dlq-topic`)\n")
	}
	b.WriteString("В DLQ попадают только нарушения схемы (missing, typedrift, invalidjson); dup, ooo и lag — валидные сообщения, остаются в основном топике и фиксируются только записями инспектора.\n\n")

	if len(r.Timeline) > 0 {
		b.WriteString("## Таймлайн\n\n")
		total := 0
		for _, bk := range r.Timeline {
			total += bk.Count
		}
		fmt.Fprintf(&b, "Распределение записей инспектора по **времени события** — отметке в самом событии, а не моменту получения (для некорректного JSON отметка неизвестна — берётся момент получения). Всего записей: %d — это все записи из раздела «Что проверяли».\n\n", total)
		clusters := TimelineClusters(r.Timeline)
		maxSum := 0
		for _, c := range clusters {
			if c.Total > maxSum {
				maxSum = c.Total
			}
		}
		for i, c := range clusters {
			if i > 0 {
				fmt.Fprintf(&b, "разрыв %dс — событий с таким временем события не было: так проявляется дефект «лаг» — события отправляются сейчас, но несут «прошлую» отметку времени, и между ними и свежими событиями образуется разрыв\n\n", c.StartS-clusters[i-1].EndS)
			}
			fmt.Fprintf(&b, "%s–%s (время события) %s %d\n",
				utcHMS(c.StartS), utcHMS(c.EndS), strings.Repeat("█", BarWidth(c.Total, maxSum)), c.Total)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Как проверять отчёт за 10 секунд\n\n")
	b.WriteString("1. Recall = 100% по всем видам? — значит, ни один заложенный дефект не просочился.\n")
	b.WriteString("2. DLQ: «совпадает»? — значит, в очереди проблемных сообщений ничего не потеряно.\n")
	b.WriteString("3. Precision = 100% у всех видов, кроме ooo (и иногда lag): у них ниже 100% — ожидаемо: «старое» событие фиксируется и как lag, и как ooo, и запись ooo не совпадает с заложенными ooo-дефектами (см. пометку после таблицы). Ниже 100% у любого другого вида — повод разбираться.\n")
	b.WriteString("4. Сошлись пункты 1–3 и в отчёте нет предупреждений (warnings о неизвестных проверках/дефектах) — в шапке будет «проблем не обнаружено»; иначе шапка назовёт причину.\n\n")

	fmt.Fprintf(&b, "---\n*Сгенерировано: %s · Данные: %s, %s*\n",
		r.GeneratedAt.UTC().Format("2006-01-02 15:04 (UTC)"), r.Inputs.Ledger, r.Inputs.Findings)
	b.WriteString("*Машиночитаемая версия: `audit-report.json`; формат для специалистов: `-format text`*\n")
	return b.String(), nil
}

func utcHMS(bucketS int64) string {
	return time.Unix(bucketS, 0).UTC().Format("15:04:05")
}

// RenderHTML renders the report as a self-contained HTML document.

const htmlStyle = `
body{font-family:system-ui,sans-serif;max-width:900px;margin:24px auto;padding:0 16px;color:#222}
table{border-collapse:collapse;margin:12px 0}
th,td{border:1px solid #ccc;padding:4px 10px;text-align:left}
td.num,th.num{text-align:right}
.verdict-ok{background:#e6f4ea;color:#0a7d2e;padding:10px;border-radius:6px;font-weight:600}
.verdict-bad{background:#fdecea;color:#c0392b;padding:10px;border-radius:6px;font-weight:600}
.metric-ok{color:#0a7d2e;font-weight:600}
.metric-warn{color:#a06b00;font-weight:600}
.metric-bad{color:#c0392b;font-weight:600}
.mismatch{color:#c0392b;font-weight:600}
.tl-row{display:flex;align-items:center;gap:8px;margin:2px 0;font-size:13px}
.tl-bar-track{flex:0 0 40%;background:#f0f0f0;border-radius:3px}
.tl-bar{height:10px;background:#4a7ebb;border-radius:3px}
.tl-gap{color:#666;font-size:12px;margin:6px 0}
`

// RenderHTML — самодостаточный HTML-отчёт с цветовыми маркерами (spec §4, §6).
func RenderHTML(r Report) (string, error) {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>Отчёт о качестве данных</title><style>")
	b.WriteString(htmlStyle)
	b.WriteString("</style></head><body>\n")
	b.WriteString("<h1>Отчёт о качестве данных (audit)</h1>\n")

	healthy, problems := Verdict(r)
	if healthy {
		b.WriteString("<p class=\"verdict-ok\">Вердикт: проблем не обнаружено.</p>\n<p>")
		if r.DLQTopic != nil {
			b.WriteString("Все заложенные дефекты найдены; сверка DLQ — совпадает.</p>\n")
		} else {
			b.WriteString("Все заложенные дефекты найдены.</p>\n")
		}
	} else {
		fmt.Fprintf(&b, "<p class=\"verdict-bad\">Вердикт: обнаружены проблемы (%d).</p>\n<ul>\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(&b, "<li>%s</li>\n", p)
		}
		b.WriteString("</ul>\n")
	}

	b.WriteString("<h2>Что проверяли</h2>\n")
	k := r.Overall.Findings - r.Overall.FalsePos - r.Overall.Caught
	if r.Overall.Findings == 0 {
		fmt.Fprintf(&b, "<p>Producer отправил %d событий, из них с заложенными дефектами: %d. Записей инспектора (consumer — читает каждое событие и фиксирует нарушения) нет.</p>\n",
			r.LedgerEntries, r.Overall.TotalDefects)
	} else {
		caughtNote := "(найдены все)"
		if r.Overall.Caught < r.Overall.TotalDefects {
			caughtNote = fmt.Sprintf("(найдено %d из %d)", r.Overall.Caught, r.Overall.TotalDefects)
		}
		fmt.Fprintf(&b, "<p>Producer отправил %d событий, из них с заложенными дефектами: %d. Инспектор (consumer — читает каждое событие и фиксирует нарушения) — записей всего %d:</p>\n<ul>\n",
			r.LedgerEntries, r.Overall.TotalDefects, r.Overall.Findings)
		fmt.Fprintf(&b, "<li>%d — заложенные дефекты %s,</li>\n", r.Overall.Caught, caughtNote)
		if k > 0 {
			fmt.Fprintf(&b, "<li>%d — дополнительные записи на те же дефекты (повторные отправки),</li>\n", k)
		}
		fmt.Fprintf(&b, "<li>%d — ложные срабатывания (не подтвердились при сверке с ledger).</li>\n</ul>\n", r.Overall.FalsePos)
		fmt.Fprintf(&b, "<p>Подтвердились записи: %d из %d — это и есть Precision.</p>\n",
			r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings)
	}

	b.WriteString("<h2>Метрики простыми словами</h2>\n<table>\n")
	b.WriteString("<tr><th>Метрика</th><th>Значение</th><th>Что это значит</th></tr>\n")
	fmt.Fprintf(&b, "<tr><td>Recall</td><td>%d/%d (%s)</td><td>Доля заложенных дефектов, которые удалось найти</td></tr>\n",
		r.Overall.Caught, r.Overall.TotalDefects, Pct(r.Overall.Recall))
	fmt.Fprintf(&b, "<tr><td>Precision</td><td>%d/%d (%s)</td><td>Доля находок, которые подтвердились; остальные — ложные срабатывания</td></tr>\n",
		r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings, Pct(r.Overall.Precision))
	b.WriteString("</table>\n")

	b.WriteString("<h2>Дефекты по видам</h2>\n<table>\n")
	b.WriteString("<tr><th>Дефект</th><th>Что это</th><th class=\"num\">Заложено</th><th class=\"num\">Найдено из заложенных</th><th class=\"num\">Recall</th><th class=\"num\">Всего записей</th><th class=\"num\">Ложных</th><th class=\"num\">Precision</th></tr>\n")
	for _, tm := range r.PerTag {
		recall, prec := "—", "—"
		recallCls, precCls := "", ""
		if tm.Total > 0 {
			recall = Pct(tm.Recall)
			if tm.Caught == tm.Total {
				recallCls = " metric-ok"
			} else {
				recallCls = " metric-bad"
			}
		}
		if tm.Findings > 0 {
			prec = Pct(tm.Precision)
			if tm.Precision < 1.0 {
				precCls = " metric-warn"
			}
		}
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num%s\">%s</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num%s\">%s</td></tr>\n",
			tm.Tag, TagDescription(tm.Tag), tm.Total, tm.Caught, recallCls, recall, tm.Findings, tm.FalsePos, precCls, prec)
	}
	b.WriteString("</table>\n")
	for _, tm := range r.PerTag {
		if (tm.Tag == "ooo" || tm.Tag == "lag") && tm.FalsePos > 0 {
			b.WriteString("<p>Событие с «старой» отметкой времени (lag) приходит и не по порядку — поэтому инспектор фиксирует его дважды: как lag и как ooo. Запись ooo не совпадает ни с одним заложенным ooo-дефектом и считается для ooo «ложной», хотя порядок действительно был нарушен. Это ожидаемое поведение демо, а не ошибка детектора.</p>\n")
			break
		}
	}
	b.WriteString("<p><strong>Как читать колонки:</strong></p>\n<ul>\n")
	b.WriteString("<li><strong>Заложено</strong> — сколько дефектов этого вида producer вживил намеренно (записи ledger).</li>\n")
	b.WriteString("<li><strong>Найдено из заложенных</strong> — сколько из них инспектор нашёл (Заложено = Найдено → Recall 100%).</li>\n")
	b.WriteString("<li><strong>Всего записей</strong> — все записи инспектора этого вида. Запись — на сообщение, а «заложено/найдено» — на дефект: один дефект может дать несколько записей (повторная отправка сообщения).</li>\n")
	b.WriteString("<li><strong>Ложных</strong> — записи, не подтвердившиеся при сверке с ledger.</li>\n</ul>\n")

	b.WriteString("<h2>DLQ (dead-letter queue) — очередь проблемных сообщений</h2>\n<p>")
	if r.DLQ.Count > 0 {
		var parts []string
		for _, tag := range tagOrder {
			check := tagToCheck[tag]
			if checks.IsSchemaViolation(check) {
				if n := r.DLQ.ByReason[check]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d (%s)", n, tag))
				}
			}
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "Должно быть: %d = %s (по находкам инспектора, без обращения к брокеру)<br>\n",
				r.DLQ.Count, strings.Join(parts, " + "))
		} else {
			fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, без обращения к брокеру)<br>\n", r.DLQ.Count)
		}
	} else {
		fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, без обращения к брокеру)<br>\n", r.DLQ.Count)
	}
	if r.DLQTopic != nil {
		fmt.Fprintf(&b, "Фактически: %d (прочитано из брокера)<br>\n", r.DLQTopic.Count)
		if m := CheckDLQTopic(r.DLQ, *r.DLQTopic); m == "" {
			b.WriteString("<span class=\"metric-ok\">Статус: совпадает</span><br>\n")
		} else {
			fmt.Fprintf(&b, "<span class=\"metric-bad mismatch\">Статус: РАСХОЖДЕНИЕ — %s</span><br>\n", strings.TrimPrefix(m, "DLQ-сверка: "))
		}
	} else {
		b.WriteString("Фактически: не считалось (требуется запущенный брокер и флаг <code>-dlq-topic</code>)<br>\n")
	}
	b.WriteString("В DLQ попадают только нарушения схемы (missing, typedrift, invalidjson); dup, ooo и lag — валидные сообщения, остаются в основном топике и фиксируются только записями инспектора.")
	b.WriteString("</p>\n")

	if len(r.Timeline) > 0 {
		b.WriteString("<h2>Таймлайн</h2>\n")
		total := 0
		for _, bk := range r.Timeline {
			total += bk.Count
		}
		fmt.Fprintf(&b, "<p>Распределение записей инспектора по времени события — отметке в самом событии, а не моменту получения (для некорректного JSON отметка неизвестна — берётся момент получения). Всего записей: %d — это все записи из раздела «Что проверяли».</p>\n", total)
		clusters := TimelineClusters(r.Timeline)
		maxSum := 0
		for _, c := range clusters {
			if c.Total > maxSum {
				maxSum = c.Total
			}
		}
		for i, c := range clusters {
			if i > 0 {
				fmt.Fprintf(&b, "<p class=\"tl-gap\">разрыв %dс — событий с таким временем события не было: так проявляется дефект «лаг» — события отправляются сейчас, но несут «прошлую» отметку времени, и между ними и свежими событиями образуется разрыв</p>\n", c.StartS-clusters[i-1].EndS)
			}
			width := 1
			if maxSum > 0 {
				width = 100 * c.Total / maxSum
				if width < 1 {
					width = 1
				}
			}
			fmt.Fprintf(&b, "<p class=\"tl-row\"><span>%s–%s (время события)</span><span class=\"tl-bar-track\"><span class=\"tl-bar\" style=\"width: %d%%\"></span></span><span>%d</span></p>\n",
				utcHMS(c.StartS), utcHMS(c.EndS), width, c.Total)
		}
	}

	b.WriteString("<h2>Как проверять отчёт за 10 секунд</h2>\n<ol>\n")
	b.WriteString("<li>Recall = 100% по всем видам? — значит, ни один заложенный дефект не просочился.</li>\n")
	b.WriteString("<li>DLQ: «совпадает»? — значит, в очереди проблемных сообщений ничего не потеряно.</li>\n")
	b.WriteString("<li>Precision = 100% у всех видов, кроме ooo (и иногда lag): у них ниже 100% — ожидаемо: «старое» событие фиксируется и как lag, и как ooo, и запись ooo не совпадает с заложенными ooo-дефектами (см. пометку после таблицы). Ниже 100% у любого другого вида — повод разбираться.</li>\n")
	b.WriteString("<li>Сошлись пункты 1–3 и в отчёте нет предупреждений (warnings о неизвестных проверках/дефектах) — в шапке будет «проблем не обнаружено»; иначе шапка назовёт причину.</li>\n</ol>\n")

	fmt.Fprintf(&b, "<hr><p><small>Сгенерировано: %s · Данные: %s, %s<br>Машиночитаемая версия: <code>audit-report.json</code>; формат для специалистов: <code>-format text</code></small></p>\n",
		r.GeneratedAt.UTC().Format("2006-01-02 15:04 (UTC)"), r.Inputs.Ledger, r.Inputs.Findings)
	b.WriteString("</body></html>\n")
	return b.String(), nil
}
