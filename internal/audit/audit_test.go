package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func build(t *testing.T, ledger, findings []string) Report {
	t.Helper()
	dir := t.TempDir()
	lp := writeLines(t, dir, "ledger.jsonl", ledger...)
	fp := writeLines(t, dir, "findings.jsonl", findings...)
	rep, err := BuildReport(lp, fp, time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	return rep
}

func perTag(rep Report, tag string) (TagMetric, bool) {
	for _, m := range rep.PerTag {
		if m.Tag == tag {
			return m, true
		}
	}
	return TagMetric{}, false
}

func TestMetricsIdeal(t *testing.T) {
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`,
			`{"seq":2,"order_id":"B","ts":"2026-09-23T10:00:01Z","defect":"none"}`,
			`{"seq":3,"order_id":"C","ts":"2026-09-23T10:00:02Z","defect":"lag"}`,
		},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"no amount","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"lag","order_id":"C","offset":2,"detail":"slow","ts":"2026-09-23T10:00:02Z"}`,
		})
	if rep.LedgerEntries != 3 {
		t.Fatalf("LedgerEntries = %d, want 3", rep.LedgerEntries)
	}
	m, _ := perTag(rep, "missing")
	if m.Total != 1 || m.Caught != 1 || m.Recall != 1.0 || m.Findings != 1 || m.FalsePos != 0 || m.Precision != 1.0 {
		t.Fatalf("missing = %+v", m)
	}
	l, _ := perTag(rep, "lag")
	if l.Total != 1 || l.Caught != 1 || l.Recall != 1.0 || l.Precision != 1.0 {
		t.Fatalf("lag = %+v", l)
	}
	if len(rep.PerTag) != 6 {
		t.Fatalf("per_tag len = %d, want 6", len(rep.PerTag))
	}
	if rep.Overall.TotalDefects != 2 || rep.Overall.Caught != 2 || rep.Overall.Recall != 1.0 ||
		rep.Overall.Findings != 2 || rep.Overall.FalsePos != 0 || rep.Overall.Precision != 1.0 {
		t.Fatalf("overall = %+v", rep.Overall)
	}
	if rep.DLQ.Count != 1 || rep.DLQ.ByReason["field_missing"] != 1 {
		t.Fatalf("dlq = %+v", rep.DLQ)
	}
	if m.FirstTS == "" || m.LastTS == "" {
		t.Fatalf("first/last ts empty: %+v", m)
	}
}

func TestFalsePositive(t *testing.T) {
	rep := build(t,
		[]string{`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"field_missing","order_id":"Z","offset":1,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
		})
	m, _ := perTag(rep, "missing")
	if m.Findings != 2 || m.FalsePos != 1 || m.Precision != 0.5 || m.Caught != 1 || m.Recall != 1.0 {
		t.Fatalf("missing = %+v", m)
	}
}

func TestCrossFire(t *testing.T) {
	// ledger tag=lag order A + findings lag(A) и out_of_order(A) →
	// caught[lag]=1, fp[ooo]=1 (кросс-срабатывание, spec §4).
	rep := build(t,
		[]string{`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"lag"}`},
		[]string{
			`{"check":"lag","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"out_of_order","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
		})
	l, _ := perTag(rep, "lag")
	if l.Caught != 1 || l.FalsePos != 0 || l.Precision != 1.0 {
		t.Fatalf("lag = %+v", l)
	}
	o, _ := perTag(rep, "ooo")
	if o.Total != 0 || o.Findings != 1 || o.FalsePos != 1 || o.Precision != 0.0 {
		t.Fatalf("ooo = %+v", o)
	}
	if rep.Overall.Recall != 1.0 || rep.Overall.Precision != 0.5 {
		t.Fatalf("overall = %+v", rep.Overall)
	}
}

func TestDupAsymmetry(t *testing.T) {
	// order A дубль ×3 (3 строки defect=dup) + 1 finding duplicate(A) →
	// total=3, caught=3 (строки ledger), recall=1.0, findings=1, fp=0, precision=1.0.
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"dup"}`,
			`{"seq":2,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"dup"}`,
			`{"seq":3,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"dup"}`,
		},
		[]string{
			`{"check":"duplicate","order_id":"A","offset":1,"detail":"seen","ts":"2026-09-23T10:00:00Z"}`,
		})
	m, _ := perTag(rep, "dup")
	if m.Total != 3 || m.Caught != 3 || m.Recall != 1.0 || m.Findings != 1 || m.FalsePos != 0 || m.Precision != 1.0 {
		t.Fatalf("dup = %+v", m)
	}
}

func TestInvalidJSONMin(t *testing.T) {
	ledger3 := []string{
		`{"seq":1,"order_id":"X","ts":"2026-09-23T10:00:00Z","defect":"invalidjson"}`,
		`{"seq":2,"order_id":"Y","ts":"2026-09-23T10:00:01Z","defect":"invalidjson"}`,
		`{"seq":3,"order_id":"Z","ts":"2026-09-23T10:00:02Z","defect":"invalidjson"}`,
	}
	// findings < total: caught=min=2, fp=0, precision=1.0
	rep := build(t, ledger3, []string{
		`{"check":"invalid_json","order_id":"","offset":0,"detail":"bad","ts":"2026-09-23T10:00:00Z"}`,
		`{"check":"invalid_json","order_id":"","offset":1,"detail":"bad","ts":"2026-09-23T10:00:01Z"}`,
	})
	m, _ := perTag(rep, "invalidjson")
	if m.Total != 3 || m.Caught != 2 || m.FalsePos != 0 || m.Precision != 1.0 {
		t.Fatalf("case1 invalidjson = %+v", m)
	}
	// findings > total: caught=3, fp=1, precision=3/4
	findings4 := make([]string, 4)
	for i := range findings4 {
		findings4[i] = `{"check":"invalid_json","order_id":"","offset":` + string(rune('0'+i)) + `,"detail":"bad","ts":"2026-09-23T10:00:00Z"}`
	}
	rep = build(t, ledger3, findings4)
	m, _ = perTag(rep, "invalidjson")
	if m.Caught != 3 || m.FalsePos != 1 || m.Precision != 0.75 {
		t.Fatalf("case2 invalidjson = %+v", m)
	}
}

func TestEmptyFiles(t *testing.T) {
	rep := build(t, nil, nil)
	if rep.Overall.Recall != 0 || rep.Overall.Precision != 0 || rep.Overall.Findings != 0 {
		t.Fatalf("overall = %+v", rep.Overall)
	}
	if len(rep.Timeline) != 0 || rep.DLQ.Count != 0 {
		t.Fatalf("timeline/dlq not empty")
	}
}

func TestDLQGrouping(t *testing.T) {
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`,
			`{"seq":2,"order_id":"B","ts":"2026-09-23T10:00:01Z","defect":"typedrift"}`,
			`{"seq":3,"order_id":"C","ts":"2026-09-23T10:00:02Z","defect":"dup"}`,
		},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"type_drift","order_id":"B","offset":1,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
			`{"check":"invalid_json","order_id":"","offset":2,"detail":"x","ts":"2026-09-23T10:00:02Z"}`,
			`{"check":"duplicate","order_id":"C","offset":2,"detail":"x","ts":"2026-09-23T10:00:02Z"}`,
		})
	if rep.DLQ.Count != 3 {
		t.Fatalf("dlq count = %d, want 3", rep.DLQ.Count)
	}
	if rep.DLQ.ByReason["field_missing"] != 1 || rep.DLQ.ByReason["type_drift"] != 1 || rep.DLQ.ByReason["invalid_json"] != 1 {
		t.Fatalf("dlq by_reason = %+v", rep.DLQ.ByReason)
	}
	if _, ok := rep.DLQ.ByReason["duplicate"]; ok {
		t.Fatalf("duplicate must not be in dlq by_reason")
	}
}

func TestTimelineBuckets(t *testing.T) {
	rep := build(t,
		[]string{`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"lag","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
			`{"check":"lag","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
		})
	if len(rep.Timeline) != 2 {
		t.Fatalf("timeline len = %d, want 2: %+v", len(rep.Timeline), rep.Timeline)
	}
	base := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC).Unix()
	if rep.Timeline[0].BucketS != base || rep.Timeline[0].Count != 1 ||
		rep.Timeline[1].BucketS != base+1 || rep.Timeline[1].Count != 2 {
		t.Fatalf("timeline = %+v", rep.Timeline)
	}
}

func TestUnknownValues(t *testing.T) {
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`,
			`{"seq":2,"order_id":"B","ts":"2026-09-23T10:00:01Z","defect":"weird"}`,
		},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"unknown_check","order_id":"B","offset":1,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
		})
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "weird") || !strings.Contains(joined, "unknown_check") {
		t.Fatalf("warnings = %q", joined)
	}
}
