package producer

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// Flush на строку: строка читаема из файла до Close (kill -9 безопасность, spec §3.4).
func TestAppendFlushesPerLine(t *testing.T) {
	path := t.TempDir() + "/ledger.jsonl"
	l, err := NewLedger(path)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	defer l.Close()
	if err := l.Append(LedgerEntry{OrderID: "o1", Ts: time.Now(), Defect: DefectNone}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"order_id":"o1"`) {
		t.Fatalf("line not flushed before Close: %q", string(data))
	}
}

func TestLedgerCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out", "nested", "ledger.jsonl")
	l, err := NewLedger(path)
	if err != nil {
		t.Fatalf("NewLedger with missing parent dirs: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ledger file not created: %v", err)
	}
}
