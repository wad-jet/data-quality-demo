package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"dqdemo/internal/audit"
)

func main() {
	ledgerPath := flag.String("ledger", "", "path to producer ledger JSONL (required)")
	findingsPath := flag.String("findings", "", "path to findings JSONL (required)")
	outPath := flag.String("out", "", "path to write JSON report (empty = off)")
	flag.Parse()

	if *ledgerPath == "" || *findingsPath == "" {
		fmt.Fprintln(os.Stderr,
			"usage: audit -ledger ledger.jsonl -findings findings.jsonl [-out report.json]")
		os.Exit(1)
	}

	rep, err := audit.BuildReport(*ledgerPath, *findingsPath, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
		os.Exit(1)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(os.Stderr, "audit: warning: %s\n", w)
	}
	fmt.Println(rep.RenderHuman())

	if *outPath != "" {
		if err := audit.WriteJSON(rep, *outPath); err != nil {
			fmt.Fprintf(os.Stderr, "audit: write %s: %v\n", *outPath, err)
			os.Exit(1)
		}
	}
}
