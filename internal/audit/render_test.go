package audit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleReportFull() Report {
	return Report{
		GeneratedAt:   time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
		Inputs:        Inputs{Ledger: "ledger.jsonl", Findings: "findings.jsonl"},
		LedgerEntries: 5,
		PerTag: []TagMetric{{
			Tag: "missing", Check: "field_missing", Total: 1, Caught: 1, Recall: 1.0,
			Findings: 1, FalsePos: 0, Precision: 1.0,
		}, {
			Tag: "dup", Check: "duplicate", Total: 2, Caught: 1, Recall: 0.5,
			Findings: 2, FalsePos: 1, Precision: 0.5,
		}},
		DLQ:      DLQ{Count: 1, ByReason: map[string]int{"field_missing": 1}},
		Timeline: []TimelineBucket{{BucketS: 1758621600, Count: 1}},
		Overall:  Overall{TotalDefects: 3, Caught: 2, Recall: 0.6667, Findings: 3, Precision: 0.6667},
		Warnings: []string{"sample warning"},
	}
}

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	rep := sampleReportFull()
	if err := WriteJSON(rep, path); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := LoadJSON(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// compare full report using DeepEqual
	if !reflect.DeepEqual(back, rep) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", back, rep)
	}
	// ensure ledger_entries field is present in JSON file
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "ledger_entries") {
		t.Fatalf("ledger_entries not serialized")
	}
}

// TestLoadJSON error cases
func TestLoadJSONSyntaxError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	// write invalid JSON
	os.WriteFile(path, []byte("{invalid json}"), 0o644)
	_, err := LoadJSON(path)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), ":") {
		t.Fatalf("error does not contain path and location: %v", err)
	}
}

func TestLoadJSONTypeError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "type.json")
	content := `{
        "generated_at": "2026-09-23T10:00:00Z",
        "inputs": {"ledger": "", "findings": ""},
        "overall": {"recall": "oops", "precision": 0.0, "caught": 0, "findings": 0, "false_positives": 0, "total_defects": 0},
        "per_tag": [], "dlq": {"count":0,"by_reason":{}}, "timeline": [], "ledger_entries": 0
    }`
	os.WriteFile(path, []byte(content), 0o644)
	_, err := LoadJSON(path)
	if err == nil {
		t.Fatalf("expected type error")
	}
	if !strings.Contains(err.Error(), "field overall.recall") && !strings.Contains(err.Error(), "field overall") {
		t.Fatalf("error does not mention field name: %v", err)
	}
}

func TestRenderMarkdown(t *testing.T) {
	out := sampleReportFull().RenderMarkdown()
	// basic checks
	for _, want := range []string{"# Audit report", "## Overall", "## Per tag", "| missing |", "| dup |", "DLQ (offline, schema-violations): 1", "## Warnings", "sample warning"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown missing %q", want)
		}
	}
}

func TestRenderMarkdownTimeline(t *testing.T) {
	out := sampleReportFull().RenderMarkdown()
	if !strings.Contains(out, "## Timeline") {
		t.Fatalf("markdown missing Timeline header")
	}
	// Expect bucket format t=<bucket>:<count>
	if !strings.Contains(out, "t=1758621600:1") {
		t.Fatalf("markdown missing timeline bucket")
	}
}

func TestRenderHTMLTimeline(t *testing.T) {
	out := sampleReportFull().RenderHTML()
	if !strings.Contains(out, "<h2>Timeline</h2>") {
		t.Fatalf("html missing Timeline header")
	}
	if !strings.Contains(out, "t=1758621600:1") {
		t.Fatalf("html missing timeline bucket")
	}
}

func TestRenderHTML(t *testing.T) {
	out := sampleReportFull().RenderHTML()
	for _, want := range []string{"<!doctype html>", "<h1>Audit report</h1>", "<table>", "<th>Recall</th>", "DLQ (offline, schema-violations): 1", ".mismatch"} {
		if !strings.Contains(out, want) {
			t.Fatalf("html missing %q", want)
		}
	}
}
