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

// RenderMarkdown renders the report as markdown.
func (r Report) RenderMarkdown() string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Audit report")
	fmt.Fprintf(&b, "Generated at: %s\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Ledger entries: %d\n", r.LedgerEntries)
	fmt.Fprintf(&b, "Inputs: ledger=%s, findings=%s\n", r.Inputs.Ledger, r.Inputs.Findings)
	fmt.Fprintln(&b, "## Overall")
	fmt.Fprintf(&b, "Recall: %.4f, Precision: %.4f, Caught: %d, Findings: %d, FP: %d\n",
		r.Overall.Recall, r.Overall.Precision, r.Overall.Caught, r.Overall.Findings, r.Overall.FalsePos)
	fmt.Fprintln(&b, "## Per tag")
	fmt.Fprintln(&b, "| Tag | Check | Total | Caught | Recall | Findings | FP | Precision |")
	fmt.Fprintln(&b, "|---|---|---|---|---|---|---|---|")
	for _, m := range r.PerTag {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %.1f%% | %d | %d | %.1f%% |\n",
			m.Tag, m.Check, m.Total, m.Caught, m.Recall*100, m.Findings, m.FalsePos, m.Precision*100)
	}
	fmt.Fprintln(&b, "## DLQ")
	fmt.Fprintf(&b, "DLQ (offline, schema-violations): %d", r.DLQ.Count)
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
	fmt.Fprintln(&b, "")
	if len(r.Warnings) > 0 {
		fmt.Fprintln(&b, "## Warnings")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	if len(r.Timeline) > 0 {
		fmt.Fprintln(&b, "## Timeline")
		var parts []string
		for _, bk := range r.Timeline {
			parts = append(parts, fmt.Sprintf("t=%d:%d", bk.BucketS, bk.Count))
		}
		fmt.Fprintln(&b, strings.Join(parts, " "))
	}
	return b.String()
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
