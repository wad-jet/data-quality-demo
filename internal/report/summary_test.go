package report

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dqdemo/internal/checks"
	"dqdemo/internal/producer"
)

func writeLedger(t *testing.T, dir string, entries []producer.LedgerEntry) string {
	t.Helper()
	path := filepath.Join(dir, "ledger.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal ledger entry: %v", err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			t.Fatalf("write ledger: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush ledger: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	return path
}

func testFindings(ts time.Time) []checks.Finding {
	return []checks.Finding{
		{Check: "field_missing", OrderID: "a-miss-1", Offset: 1, Detail: "d", Ts: ts},
		{Check: "type_drift", Offset: 2, Detail: "d", Ts: ts},
		{Check: "duplicate", OrderID: "o-dup-1", Offset: 3, Detail: "d", Ts: ts},
		{Check: "out_of_order", OrderID: "o-ooo-1", Offset: 4, Detail: "d", Ts: ts},
		{Check: "lag", OrderID: "o-lag-1", Offset: 5, Detail: "d", Ts: ts},
		{Check: "invalid_json", Offset: 6, Detail: "d", Ts: ts},
	}
}

func TestSummaryRenderWithLedger(t *testing.T) {
	ts := time.Date(2026, 9, 23, 14, 0, 0, 0, time.UTC)
	s := NewSummary()
	for _, f := range testFindings(ts) {
		s.AddFinding(f)
	}
	s.AddDLQ()
	s.AddDLQ()
	s.AddDLQ()

	dir := t.TempDir()
	ledger := writeLedger(t, dir, []producer.LedgerEntry{
		{Seq: 1, OrderID: "a-miss-1", Ts: ts, Defect: producer.DefectMissing},
		{Seq: 2, OrderID: "a-miss-2", Ts: ts, Defect: producer.DefectMissing}, // не пойман
		{Seq: 3, OrderID: "o-dup-1", Ts: ts, Defect: producer.DefectDup},
		{Seq: 4, OrderID: "o-drift-1", Ts: ts, Defect: producer.DefectTypeDrift}, // не пойман
		{Seq: 5, OrderID: "o-ooo-1", Ts: ts, Defect: producer.DefectOOO},
		{Seq: 6, OrderID: "o-lag-1", Ts: ts, Defect: producer.DefectLag},
		{Seq: 7, OrderID: "", Ts: ts, Defect: producer.DefectInvalidJSON},
		{Seq: 8, OrderID: "", Ts: ts, Defect: producer.DefectInvalidJSON}, // caught = min(1 finding, 2 total) = 1
		{Seq: 9, OrderID: "clean-1", Ts: ts, Defect: producer.DefectNone}, // игнорируется
	})
	if err := s.LoadLedger(ledger); err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	s.ComputeCaught(testFindings(ts))

	got := s.Render()
	t.Logf("rendered:\n%s", got)

	wantTotal := "Findings: total=6 | field_missing=1 type_drift=1 duplicate=1 out_of_order=1 lag=1 invalid_json=1"
	if !strings.Contains(got, wantTotal) {
		t.Errorf("missing line %q in:\n%s", wantTotal, got)
	}
	if !strings.Contains(got, "DLQ: 3 (dlq_errors=0)") {
		t.Errorf("missing DLQ line in:\n%s", got)
	}
	for _, want := range []string{
		"missing=1/2", // a-miss-1 пойман, a-miss-2 нет
		"dup=1/1",
		"typedrift=0/1", // finding type_drift без order_id → не пойман
		"ooo=1/1",
		"lag=1/1",
		"invalidjson=1/2", // min(1, 2)
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestSummaryNoLedgerNoCaughtLine(t *testing.T) {
	s := NewSummary()
	ts := time.Now().UTC()
	s.AddFinding(checks.Finding{Check: "lag", OrderID: "o-1", Ts: ts})
	got := s.Render()
	if strings.Contains(got, "Caught/total") {
		t.Errorf("caught line should not be rendered without ledger:\n%s", got)
	}
}

func TestSummaryInvalidJSONCaughtMin(t *testing.T) {
	ts := time.Now().UTC()
	s := NewSummary()
	in := []checks.Finding{
		{Check: "invalid_json", Offset: 1, Ts: ts},
		{Check: "invalid_json", Offset: 2, Ts: ts},
		{Check: "invalid_json", Offset: 3, Ts: ts},
	}
	for _, f := range in {
		s.AddFinding(f)
	}

	dir := t.TempDir()
	ledger := writeLedger(t, dir, []producer.LedgerEntry{
		{Seq: 1, OrderID: "", Ts: ts, Defect: producer.DefectInvalidJSON},
		{Seq: 2, OrderID: "", Ts: ts, Defect: producer.DefectInvalidJSON},
	})
	if err := s.LoadLedger(ledger); err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	s.ComputeCaught(in)
	if got := s.Caught["invalidjson"]; got != 2 {
		t.Errorf("invalidjson caught = %d, want 2 (min of 3 findings and 2 ledger entries)", got)
	}
	if got := s.TagTotal["invalidjson"]; got != 2 {
		t.Errorf("invalidjson total = %d, want 2", got)
	}
}

func TestLoadLedgerMissingFile(t *testing.T) {
	s := NewSummary()
	if err := s.LoadLedger(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("LoadLedger with missing file: want error, got nil")
	}
}
