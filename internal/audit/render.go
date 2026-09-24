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
func (r Report) RenderHTML() string {
	var b strings.Builder
	fmt.Fprintln(&b, "<!doctype html>")
	fmt.Fprintln(&b, "<html><head><meta charset=\"utf-8\"><title>Audit report</title><style>")
	fmt.Fprintln(&b, ".mismatch { background-color: #ffdddd; } table, th, td { border: 1px solid #ccc; border-collapse: collapse; padding: 4px; }")
	fmt.Fprintln(&b, "</style></head><body>")
	fmt.Fprintln(&b, "<h1>Audit report</h1>")
	fmt.Fprintf(&b, "<p>Generated at: %s</p>\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "<p>Ledger entries: %d</p>\n", r.LedgerEntries)
	fmt.Fprintf(&b, "<p>Inputs: ledger=%s, findings=%s</p>\n", r.Inputs.Ledger, r.Inputs.Findings)
	fmt.Fprintln(&b, "<h2>Overall</h2>")
	fmt.Fprintf(&b, "<p>Recall: %.4f, Precision: %.4f, Caught: %d, Findings: %d, FP: %d</p>\n",
		r.Overall.Recall, r.Overall.Precision, r.Overall.Caught, r.Overall.Findings, r.Overall.FalsePos)
	fmt.Fprintln(&b, "<h2>Per tag</h2>")
	fmt.Fprintln(&b, "<table><thead><tr><th>Tag</th><th>Check</th><th>Total</th><th>Caught</th><th>Recall</th><th>Findings</th><th>FP</th><th>Precision</th></tr></thead><tbody>")
	for _, m := range r.PerTag {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%.1f%%</td><td>%d</td><td>%d</td><td>%.1f%%</td></tr>\n",
			m.Tag, m.Check, m.Total, m.Caught, m.Recall*100, m.Findings, m.FalsePos, m.Precision*100)
	}
	fmt.Fprintln(&b, "</tbody></table>")
	fmt.Fprintln(&b, "<h2>DLQ</h2>")
	fmt.Fprintf(&b, "<p>DLQ (offline, schema-violations): %d", r.DLQ.Count)
	if len(r.DLQ.ByReason) > 0 {
		var parts []string
		for _, reason := range dlqReasonOrder {
			if n := r.DLQ.ByReason[reason]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", reason, n))
			}
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(parts, " "))
		}
	}
	fmt.Fprintln(&b, "</p>")
	if full, mismatch, present := r.dlqTopicLine(); present {
		cls := ""
		if mismatch {
			cls = " class=\"mismatch\""
		}
		fmt.Fprintf(&b, "<p%s>%s</p>\n", cls, full)
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintln(&b, "<h2>Warnings</h2><ul>")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "<li>%s</li>", w)
		}
		fmt.Fprintln(&b, "</ul>")
	}
	if len(r.Timeline) > 0 {
		fmt.Fprintln(&b, "<h2>Timeline</h2>")
		var parts []string
		for _, bk := range r.Timeline {
			parts = append(parts, fmt.Sprintf("t=%d:%d", bk.BucketS, bk.Count))
		}
		fmt.Fprintf(&b, "<p>Timeline (1s buckets): %s</p>\n", strings.Join(parts, " "))
	}
	fmt.Fprintln(&b, "</body></html>")
	return b.String()
}
