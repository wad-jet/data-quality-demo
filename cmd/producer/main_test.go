package main

import (
	"testing"

	"dqdemo/internal/checks"
	"dqdemo/internal/report"
)

func TestDQField(t *testing.T) {
	s := report.NewSummary()
	s.AddFinding(checks.Finding{Check: "lag"})
	s.AddFinding(checks.Finding{Check: "lag"})
	s.AddFinding(checks.Finding{Check: "duplicate"})
	want := "field_missing=0 type_drift=0 duplicate=1 out_of_order=0 lag=2 invalid_json=0"
	if got := dqField(s); got != want {
		t.Fatalf("dqField = %q, want %q", got, want)
	}
}
