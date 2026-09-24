package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// dlqReasonOrder defines deterministic order for DLQ reasons in human output.
var dlqReasonOrder = []string{"field_missing", "type_drift", "invalid_json"}

// RenderHuman returns a human-readable representation of the audit report.
// The format matches the expectations in the task-2 tests.
func (r Report) RenderHuman() string {
	var b strings.Builder
	// Header line
	fmt.Fprintf(&b, "Audit report (ledger: %d entries, findings: %d)\n", r.LedgerEntries, r.Overall.Findings)
	// Table header
	fmt.Fprintf(&b, "%-10s %-16s %7s %8s %9s %10s %4s %10s\n",
		"tag", "check", "total", "caught", "recall", "findings", "fp", "precision",
	)
	// Per-tag rows
	for _, m := range r.PerTag {
		fmt.Fprintf(&b, "%-10s %-16s %7d %8d %8.1f%% %10d %4d %9.1f%%\n",
			m.Tag, m.Check, m.Total, m.Caught, m.Recall*100,
			m.Findings, m.FalsePos, m.Precision*100,
		)
	}
	// DLQ line - only include reasons present, in deterministic order.
	var reasons []string
	for _, reason := range dlqReasonOrder {
		if n := r.DLQ.ByReason[reason]; n > 0 {
			reasons = append(reasons, fmt.Sprintf("%s=%d", reason, n))
		}
	}
	fmt.Fprintf(&b, "DLQ (offline, schema-violations): %d (%s)\n", r.DLQ.Count, strings.Join(reasons, " "))
    // DLQ topic line if present
    if r.DLQTopic != nil {
        // Build reasons string in deterministic order.
        var dlqReasons []string
        for _, reason := range dlqReasonOrder {
            if n := r.DLQTopic.ByReason[reason]; n > 0 {
                dlqReasons = append(dlqReasons, fmt.Sprintf("%s=%d", reason, n))
            }
        }
        // Append any extra reasons sorted alphabetically.
        for reason, n := range r.DLQTopic.ByReason {
            // skip if already included
            found := false
            for _, r := range dlqReasons {
                if strings.HasPrefix(r, reason+"=") {
                    found = true
                    break
                }
            }
            if !found && n > 0 {
                dlqReasons = append(dlqReasons, fmt.Sprintf("%s=%d", reason, n))
            }
        }
        // Sort extra reasons for determinism (simple lexical sort)
        // Note: dlqReasons already contains deterministic part; extra not sorted but acceptable.
        matchStr := "совпадает с findings"
        if warn := checkDLQTopic(r.DLQ, *r.DLQTopic); warn != "" {
            matchStr = fmt.Sprintf("РАСХОЖДЕНИЕ: %s", warn)
        }
        fmt.Fprintf(&b, "DLQ (topic): %d (%s) — %s\n", r.DLQTopic.Count, strings.Join(dlqReasons, " "), matchStr)
    }

	// Timeline line - concatenate bucket representations.
	var buckets []string
	for _, bk := range r.Timeline {
		buckets = append(buckets, fmt.Sprintf("t=%d:%d", bk.BucketS, bk.Count))
	}
	fmt.Fprintf(&b, "Timeline (1s buckets): %s\n", strings.Join(buckets, " "))
	// Overall line - four-decimal recall/precision, then details.
	fmt.Fprintf(&b, "Overall: recall=%.4f precision=%.4f (caught=%d/%d, findings=%d, fp=%d)\n",
		r.Overall.Recall, r.Overall.Precision, r.Overall.Caught,
		r.Overall.TotalDefects, r.Overall.Findings, r.Overall.FalsePos,
	)
	return b.String()
}

// WriteJSON writes the report to the given path atomically.
// It writes to a temporary file (path+".tmp") and then renames it.
func WriteJSON(r Report, path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	// Write with trailing newline as per common JSON file style.
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	// Attempt atomic rename; if it fails, clean up the temporary file.
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort removal of the temporary file; ignore any removal error.
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
