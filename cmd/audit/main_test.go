package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dqdemo/internal/audit"
)

// fixedReport — фиксированный Report для round-trip через WriteJSON/LoadJSON.
func fixedReport() audit.Report {
	return audit.Report{
		GeneratedAt: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
		Inputs:      audit.Inputs{Ledger: "ledger.jsonl", Findings: "findings.jsonl"},
		Overall: audit.Overall{
			TotalDefects: 2, Caught: 2, Recall: 1,
			Findings: 2, Precision: 1,
		},
		PerTag: []audit.TagMetric{
			{Tag: "missing", Check: "field_missing", Total: 1, Caught: 1, Recall: 1, Findings: 1, Precision: 1},
			{Tag: "dup", Check: "duplicate", Total: 1, Caught: 1, Recall: 1, Findings: 1, Precision: 1},
		},
		DLQ:           audit.DLQ{Count: 0},
		LedgerEntries: 4,
	}
}

func TestRunAudit(t *testing.T) {
	dir := t.TempDir()
	reportJSON := filepath.Join(dir, "audit-report.json")
	if err := audit.WriteJSON(fixedReport(), reportJSON); err != nil {
		t.Fatalf("fixture WriteJSON: %v", err)
	}
	rep, err := audit.LoadJSON(reportJSON)
	if err != nil {
		t.Fatalf("fixture LoadJSON: %v", err)
	}
	md := rep.RenderMarkdown()
	html := rep.RenderHTML()
	human := rep.RenderHuman()

	outMd := filepath.Join(dir, "report.md")

	tests := []struct {
		name     string
		args     []string
		code     int
		stdout   string // точное ожидаемое stdout
		outFile  string // если задан — файл должен существовать с содержимым wantFile
		wantFile string
		// notUsage — stderr должен НЕ содержать "usage:" (ошибка не usage, а рабочего режима)
		notUsage bool
	}{
		{
			name:   "from и ledger одновременно — usage-ошибка",
			args:   []string{"-from", reportJSON, "-ledger", "l.jsonl"},
			code:   1,
			stdout: "",
		},
		{
			name:   "format md без from — usage-ошибка",
			args:   []string{"-format", "md"},
			code:   1,
			stdout: "",
		},
		{
			name:   "format html без from — usage-ошибка",
			args:   []string{"-format", "html"},
			code:   1,
			stdout: "",
		},
		{
			name:   "from md без out — markdown в stdout",
			args:   []string{"-from", reportJSON, "-format", "md"},
			code:   0,
			stdout: md + "\n",
		},
		{
			name:     "from md с out — файл, stdout пуст",
			args:     []string{"-from", reportJSON, "-format", "md", "-out", outMd},
			code:     0,
			stdout:   "",
			outFile:  outMd,
			wantFile: md,
		},
		{
			name:   "from html — html в stdout",
			args:   []string{"-from", reportJSON, "-format", "html"},
			code:   0,
			stdout: html + "\n",
		},
		{
			name:   "from text — human-рендер в stdout (default-формат)",
			args:   []string{"-from", reportJSON},
			code:   0,
			stdout: human + "\n",
		},
		{
			name:   "from с неизвестным форматом — usage-ошибка",
			args:   []string{"-from", reportJSON, "-format", "xml"},
			code:   1,
			stdout: "",
		},
		{
			name:   "build-режим: format игнорируется (не usage-ошибка)",
			args:   []string{"-ledger", "nope.jsonl", "-findings", "nope.jsonl", "-format", "md"},
			code:   1, // BuildReport: файлов нет
			stdout: "",
			// stdout должен быть пуст и stderr — не usage (ошибка BuildReport)
			notUsage: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runAudit(tc.args, &stdout, &stderr)

			if code != tc.code {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tc.code, stderr.String())
			}
			if got := stdout.String(); got != tc.stdout {
				t.Errorf("stdout = %q, want %q", got, tc.stdout)
			}
			if tc.notUsage && strings.Contains(stderr.String(), "usage:") {
				t.Errorf("stderr содержит usage-сообщение, а должна быть рабочая ошибка: %q", stderr.String())
			}
			if tc.outFile != "" {
				data, err := os.ReadFile(tc.outFile)
				if err != nil {
					t.Fatalf("file %s: %v", tc.outFile, err)
				}
				if string(data) != tc.wantFile {
					t.Errorf("file content = %q, want %q", string(data), tc.wantFile)
				}
			}
		})
	}
}

// TestRunAuditDLQDeadBroker verifies that a dead bootstrap causes dlq error exit.
func TestRunAuditDLQDeadBroker(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	findingsPath := filepath.Join(dir, "findings.jsonl")
	if err := os.WriteFile(ledgerPath, []byte{}, 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	if err := os.WriteFile(findingsPath, []byte{}, 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	args := []string{"-ledger", ledgerPath, "-findings", findingsPath, "-dlq-topic", "some.topic", "-bootstrap", addr}
	var outBuf, errBuf bytes.Buffer
	code := runAudit(args, &outBuf, &errBuf)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(errBuf.String(), "audit: dlq:") {
		t.Fatalf("stderr missing dlq error, got %q", errBuf.String())
	}
}

// TestRunAuditHelp: -h/-help must exit 0 (help is not an error).
func TestRunAuditHelp(t *testing.T) {
	for _, arg := range []string{"-h", "-help"} {
		var outBuf, errBuf bytes.Buffer
		code := runAudit([]string{arg}, &outBuf, &errBuf)
		if code != 0 {
			t.Fatalf("%s: expected exit 0, got %d (stderr: %q)", arg, code, errBuf.String())
		}
		if outBuf.String() != "" {
			t.Fatalf("%s: expected empty stdout, got %q", arg, outBuf.String())
		}
	}
}
