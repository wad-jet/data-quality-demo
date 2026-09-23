package report

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dqdemo/internal/checks"
)

func TestFindingsWriterAppendsValidJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	w, err := NewFindingsWriter(path)
	if err != nil {
		t.Fatalf("NewFindingsWriter: %v", err)
	}
	ts := time.Date(2026, 9, 23, 14, 0, 0, 0, time.UTC)
	in := []checks.Finding{
		{Check: "field_missing", OrderID: "o-1", Offset: 10, Detail: "missing: amount", Ts: ts},
		{Check: "duplicate", OrderID: "o-2", Offset: 11, Detail: "dup o-2", Ts: ts},
		{Check: "lag", Offset: 12, Detail: "lag 5m", Ts: ts},
	}
	for _, f := range in {
		if err := w.Append(f); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open result file: %v", err)
	}
	defer fh.Close()

	sc := bufio.NewScanner(fh)
	var got []checks.Finding
	for sc.Scan() {
		var f checks.Finding
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", sc.Text(), err)
		}
		got = append(got, f)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %d", len(got))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("line %d: got %+v, want %+v", i, got[i], in[i])
		}
	}
}
