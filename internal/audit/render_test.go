package audit

import (

    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func sampleReportFull() Report {
    return Report{
        GeneratedAt: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
        Inputs:      Inputs{Ledger: "ledger.jsonl", Findings: "findings.jsonl"},
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
    // compare important fields
    if back.LedgerEntries != rep.LedgerEntries || back.Overall.Caught != rep.Overall.Caught {
        t.Fatalf("round-trip mismatch: %+v vs %+v", back, rep)
    }
    if back.GeneratedAt.IsZero() || !back.GeneratedAt.Equal(rep.GeneratedAt) {
        t.Fatalf("generated_at mismatch")
    }
    // ensure ledger_entries field is present in JSON file
    data, _ := os.ReadFile(path)
    if !strings.Contains(string(data), "ledger_entries") {
        t.Fatalf("ledger_entries not serialized")
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

func TestRenderHTML(t *testing.T) {
    out := sampleReportFull().RenderHTML()
    for _, want := range []string{"<!doctype html>", "<h1>Audit report</h1>", "<table>", "<th>Recall</th>", "DLQ (offline, schema-violations): 1", ".mismatch"} {
        if !strings.Contains(out, want) {
            t.Fatalf("html missing %q", want)
        }
    }
}
