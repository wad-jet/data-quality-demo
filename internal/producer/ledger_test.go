package producer

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLedgerAppendAndClose(t *testing.T) {
	tmp, err := os.CreateTemp("", "ledger_test_*.log")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	l, err := NewLedger(path)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	now := time.Now()
	entries := []LedgerEntry{{OrderID: "o-000001", Ts: now, Defect: DefectNone}, {OrderID: "o-000002", Ts: now.Add(time.Second), Defect: DefectMissing}, {OrderID: "o-000003", Ts: now.Add(2 * time.Second), Defect: DefectDup}}
	for _, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// read file lines
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	i := 1
	for scanner.Scan() {
		var le LedgerEntry
		if err := json.Unmarshal(scanner.Bytes(), &le); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if le.Seq != int64(i) {
			t.Fatalf("seq mismatch: got %d want %d", le.Seq, i)
		}
		i++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if i-1 != len(entries) {
		t.Fatalf("line count mismatch: got %d want %d", i-1, len(entries))
	}
}
