package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
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
	fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Инспектор нашёл %d записей: %d — реальные дефекты, %d — ложные срабатывания.\n\n",
		r.LedgerEntries, r.Overall.TotalDefects, r.Overall.Findings, r.Overall.Caught, r.Overall.FalsePos)

	b.WriteString("## Метрики простыми словами\n\n")
	b.WriteString("| Метрика | Значение | Что это значит |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Recall | %d/%d (%s) | Доля заложенных дефектов, которые удалось найти |\n",
		r.Overall.Caught, r.Overall.TotalDefects, Pct(r.Overall.Recall))
	fmt.Fprintf(&b, "| Precision | %d/%d (%s) | Доля находок, которые подтвердились; остальные — ложные срабатывания |\n",
		r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings, Pct(r.Overall.Precision))
	b.WriteString("\n")

	b.WriteString("## Дефекты по видам\n\n")
	b.WriteString("| Дефект | Что это | Заложено | Найдено | Recall | Записей | Ложных | Precision |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
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
			b.WriteString("«Старое» событие (лаг) выглядит и как «не по порядку» — одна причина, две записи; это ожидаемое поведение демо, а не ошибка.\n\n")
			break
		}
	}

	b.WriteString("## DLQ — очередь проблемных сообщений\n\n")
	fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, offline)\n", r.DLQ.Count)
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
	b.WriteString("\n")

	if len(r.Timeline) > 0 {
		b.WriteString("## Таймлайн\n\n")
		clusters := TimelineClusters(r.Timeline)
		maxSum := 0
		for _, c := range clusters {
			if c.Total > maxSum {
				maxSum = c.Total
			}
		}
		for i, c := range clusters {
			if i > 0 {
				fmt.Fprintf(&b, "разрыв %dс — как правило, дефект «лаг»: события со «старой» отметкой времени\n\n", c.StartS-clusters[i-1].EndS)
			}
			fmt.Fprintf(&b, "%s–%s (время события) %s %d\n",
				utcHMS(c.StartS), utcHMS(c.EndS), strings.Repeat("█", BarWidth(c.Total, maxSum)), c.Total)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Как проверять отчёт за 10 секунд\n\n")
	b.WriteString("1. Recall = 100% по всем видам? — значит, ни один заложенный дефект не просочился.\n")
	b.WriteString("2. DLQ: «совпадает»? — значит, в очереди проблемных сообщений ничего не потеряно.\n")
	b.WriteString("3. Precision ниже 100% у ooo/lag — это ожидаемо (двойные срабатывания); у остальных видов — 100%.\n")
	b.WriteString("4. Вердикт в шапке сводит всё в одну строку.\n\n")

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

	b.WriteString("<h2>Что проверяли</h2>\n<p>")
	fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Инспектор нашёл %d записей: %d — реальные дефекты, %d — ложные срабатывания.</p>\n",
		r.LedgerEntries, r.Overall.TotalDefects, r.Overall.Findings, r.Overall.Caught, r.Overall.FalsePos)

	b.WriteString("<h2>Метрики простыми словами</h2>\n<table>\n")
	b.WriteString("<tr><th>Метрика</th><th>Значение</th><th>Что это значит</th></tr>\n")
	fmt.Fprintf(&b, "<tr><td>Recall</td><td>%d/%d (%s)</td><td>Доля заложенных дефектов, которые удалось найти</td></tr>\n",
		r.Overall.Caught, r.Overall.TotalDefects, Pct(r.Overall.Recall))
	fmt.Fprintf(&b, "<tr><td>Precision</td><td>%d/%d (%s)</td><td>Доля находок, которые подтвердились; остальные — ложные срабатывания</td></tr>\n",
		r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings, Pct(r.Overall.Precision))
	b.WriteString("</table>\n")

	b.WriteString("<h2>Дефекты по видам</h2>\n<table>\n")
	b.WriteString("<tr><th>Дефект</th><th>Что это</th><th class=\"num\">Заложено</th><th class=\"num\">Найдено</th><th class=\"num\">Recall</th><th class=\"num\">Записей</th><th class=\"num\">Ложных</th><th class=\"num\">Precision</th></tr>\n")
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
			b.WriteString("<p>«Старое» событие (лаг) выглядит и как «не по порядку» — одна причина, две записи; это ожидаемое поведение демо, а не ошибка.</p>\n")
			break
		}
	}

	b.WriteString("<h2>DLQ — очередь проблемных сообщений</h2>\n<p>")
	fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, offline)<br>\n", r.DLQ.Count)
	if r.DLQTopic != nil {
		fmt.Fprintf(&b, "Фактически: %d (прочитано из брокера)<br>\n", r.DLQTopic.Count)
		if m := CheckDLQTopic(r.DLQ, *r.DLQTopic); m == "" {
			b.WriteString("<span class=\"metric-ok\">Статус: совпадает</span>")
		} else {
			fmt.Fprintf(&b, "<span class=\"metric-bad mismatch\">Статус: РАСХОЖДЕНИЕ — %s</span>", strings.TrimPrefix(m, "DLQ-сверка: "))
		}
	} else {
		b.WriteString("Фактически: не считалось (требуется запущенный брокер и флаг <code>-dlq-topic</code>)")
	}
	b.WriteString("</p>\n")

	if len(r.Timeline) > 0 {
		b.WriteString("<h2>Таймлайн</h2>\n")
		clusters := TimelineClusters(r.Timeline)
		maxSum := 0
		for _, c := range clusters {
			if c.Total > maxSum {
				maxSum = c.Total
			}
		}
		for i, c := range clusters {
			if i > 0 {
				fmt.Fprintf(&b, "<p class=\"tl-gap\">разрыв %dс — как правило, дефект «лаг»: события со «старой» отметкой времени</p>\n", c.StartS-clusters[i-1].EndS)
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
	b.WriteString("<li>Precision ниже 100% у ooo/lag — это ожидаемо (двойные срабатывания); у остальных видов — 100%.</li>\n")
	b.WriteString("<li>Вердикт в шапке сводит всё в одну строку.</li>\n</ol>\n")

	fmt.Fprintf(&b, "<hr><p><small>Сгенерировано: %s · Данные: %s, %s<br>Машиночитаемая версия: <code>audit-report.json</code>; формат для специалистов: <code>-format text</code></small></p>\n",
		r.GeneratedAt.UTC().Format("2006-01-02 15:04 (UTC)"), r.Inputs.Ledger, r.Inputs.Findings)
	b.WriteString("</body></html>\n")
	return b.String(), nil
}
