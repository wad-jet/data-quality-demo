package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"dqdemo/internal/audit"
)

const usage = `usage: audit -ledger ledger.jsonl -findings findings.jsonl [-out report.json]
       audit -from audit-report.json -format text|md|html [-out file]`

func main() {
	os.Exit(runAudit(os.Args[1:], os.Stdout, os.Stderr))
}

// runAudit — обработка флагов и запуск build/render-режима (тестируемое ядро CLI).
// Возвращает exit code.
func runAudit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ledgerPath := fs.String("ledger", "", "path to producer ledger JSONL (required в build-режиме)")
	findingsPath := fs.String("findings", "", "path to findings JSONL (required в build-режиме)")
	fromPath := fs.String("from", "", "path to audit JSON report (render-режим)")
	format := fs.String("format", "text", "render format for -from: text|md|html")
	outPath := fs.String("out", "", "path to write output (build: JSON report, render: rendered file)")
	dlqTopic := fs.String("dlq-topic", "", "DLQ topic name (optional, build mode only)")
	bootstrapDefault := os.Getenv("DQ_BOOTSTRAP")
	if bootstrapDefault == "" {
		bootstrapDefault = "localhost:9092"
	}
	bootstrap := fs.String("bootstrap", bootstrapDefault, "Kafka bootstrap server (build mode; defaults to $DQ_BOOTSTRAP or localhost:9092)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	usageErr := func() int {
		fmt.Fprintln(stderr, usage)
		return 1
	}

	// Взаимоисключения (spec §4: единый код usage-ошибок).
	if *fromPath != "" && (*ledgerPath != "" || *findingsPath != "") {
		return usageErr()
	}

	// Render-режим: LoadJSON → рендер по -format → stdout или файл.
	if *fromPath != "" {
		switch *format {
		case "text", "md", "html":
		default:
			return usageErr()
		}
		rep, err := audit.LoadJSON(*fromPath)
		if err != nil {
			fmt.Fprintf(stderr, "audit: load: %v\n", err)
			return 1
		}
		var rendered string
		switch *format {
		case "md":
			var err error
			rendered, err = audit.RenderMarkdown(rep)
			if err != nil {
				fmt.Fprintf(stderr, "audit: render md: %v\n", err)
				return 1
			}
		case "html":
			rendered = rep.RenderHTML()

		default: // text
			rendered = rep.RenderHuman()
		}
		if *outPath == "" {
			fmt.Fprintln(stdout, rendered)
			return 0
		}
		if err := writeFileAtomic(rendered, *outPath); err != nil {
			fmt.Fprintf(stderr, "audit: write %s: %v\n", *outPath, err)
			return 1
		}
		return 0
	}

	// Build-режим: -format игнорируется (одна точка рендера: всегда text+JSON -out).
	if *ledgerPath == "" || *findingsPath == "" {
		return usageErr()
	}
	rep, err := audit.BuildReport(*ledgerPath, *findingsPath, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "audit: %v\n", err)
		return 1
	}
	// DLQ integration (build mode only)
	if *dlqTopic != "" {
		actual, warns, err := audit.ConsumeDLQ(context.Background(), *bootstrap, *dlqTopic)
		if err != nil {
			fmt.Fprintf(stderr, "audit: dlq: %v\n", err)
			return 1
		}
		rep.DLQTopic = &actual
		rep.Warnings = append(rep.Warnings, warns...)
		if w := audit.CheckDLQTopic(rep.DLQ, actual); w != "" {
			rep.Warnings = append(rep.Warnings, w)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(stderr, "audit: warning: %s\n", w)
	}
	fmt.Fprintln(stdout, rep.RenderHuman())

	if *outPath != "" {
		if err := audit.WriteJSON(rep, *outPath); err != nil {
			fmt.Fprintf(stderr, "audit: write %s: %v\n", *outPath, err)
			return 1
		}
	}
	return 0
}

// writeFileAtomic — запись через tmp+rename (паттерн audit.WriteJSON, spec §4).
func writeFileAtomic(data, path string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
