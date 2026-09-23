package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleReport() Report {
	rep := Report{
		GeneratedAt:   time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
		Inputs:        Inputs{Ledger: "ledger.jsonl", Findings: "findings.jsonl"},
		LedgerEntries: 3,
		PerTag: []TagMetric{{
			Tag: "missing", Check: "field_missing", Total: 1, Caught: 1, Recall: 1.0,
			Findings: 1, FalsePos: 0, Precision: 1.0,
			FirstTS: "2026-09-23T10:00:00Z", LastTS: "2026-09-23T10:00:00Z",
		}},
		DLQ:      DLQ{Count: 1, ByReason: map[string]int{"field_missing": 1}},
		Timeline: []TimelineBucket{{BucketS: 1758621600, Count: 1}},
		Overall:  Overall{TotalDefects: 1, Caught: 1, Recall: 1.0, Findings: 1, Precision: 1.0},
	}
	return rep
}

func TestRenderHuman(t *testing.T) {
	out := sampleReport().RenderHuman()
	for _, want := range []string{
		"Audit report (ledger: 3 entries, findings: 1)",
		"tag", "check", "total", "caught", "recall", "findings", "fp", "precision",
		"missing", "field_missing", "100.0%",
		"DLQ (offline, schema-violations): 1 (field_missing=1)",
		"Timeline (1s buckets): t=1758621600:1",
		"Overall: recall=1.0000 precision=1.0000 (caught=1/1, findings=1, fp=0)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output missing %q:\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 7 {
		t.Fatalf("expected 7 lines, got %d:\n%s", len(lines), out)
	}
}

func TestWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := WriteJSON(sampleReport(), path); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file must be renamed away")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var back Report
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Overall.Caught != 1 || len(back.PerTag) != 1 || back.PerTag[0].Tag != "missing" ||
		back.DLQ.ByReason["field_missing"] != 1 || len(back.Timeline) != 1 {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if !back.GeneratedAt.Equal(sampleReport().GeneratedAt) {
		t.Fatalf("generated_at mismatch: %v", back.GeneratedAt)
	}
	if strings.Contains(string(data), "ledger_entries") {
		t.Fatalf("LedgerEntries must not be serialized (json:-)")
	}
}

func TestWriteJSONError(t *testing.T) {
	if err := WriteJSON(sampleReport(), "/nonexistent-dir-xyz/r.json"); err == nil {
		t.Fatalf("expected error for bad path")
	}
}
