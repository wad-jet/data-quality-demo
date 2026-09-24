// Package audit — независимый E2E-аудит качества по ledger+findings
// (spec: docs/superpowers/specs/2026-09-23-audit-tool-design.md).
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"dqdemo/internal/checks"
	"dqdemo/internal/producer"
	"dqdemo/internal/report"
)

// Тег ledger → имя check (зеркало report.tagToCheck).
var tagToCheck = map[string]string{
	"missing":     "field_missing",
	"dup":         "duplicate",
	"typedrift":   "type_drift",
	"ooo":         "out_of_order",
	"lag":         "lag",
	"invalidjson": "invalid_json",
}

// Фиксированный порядок вывода per_tag (spec §7).
var tagOrder = []string{"missing", "dup", "typedrift", "ooo", "lag", "invalidjson"}

var validDefects = map[string]struct{}{
	"none": {}, "missing": {}, "dup": {}, "typedrift": {}, "ooo": {}, "lag": {}, "invalidjson": {},
}

var validChecks = map[string]struct{}{
	"field_missing": {}, "type_drift": {}, "duplicate": {},
	"out_of_order": {}, "lag": {}, "invalid_json": {},
}

type Inputs struct {
	Ledger   string `json:"ledger"`
	Findings string `json:"findings"`
}

type TagMetric struct {
	Tag       string  `json:"tag"`
	Check     string  `json:"check"`
	Total     int     `json:"total"`
	Caught    int     `json:"caught"`
	Recall    float64 `json:"recall"`
	Findings  int     `json:"findings"`
	FalsePos  int     `json:"false_positives"`
	Precision float64 `json:"precision"`
	FirstTS   string  `json:"first_ts,omitempty"`
	LastTS    string  `json:"last_ts,omitempty"`
}

type DLQ struct {
	Count    int            `json:"count"`
	ByReason map[string]int `json:"by_reason"`
}

type TimelineBucket struct {
	BucketS int64 `json:"bucket_s"`
	Count   int   `json:"count"`
}

type Overall struct {
	TotalDefects int     `json:"total_defects"`
	Caught       int     `json:"caught"`
	Recall       float64 `json:"recall"`
	Findings     int     `json:"findings"`
	FalsePos     int     `json:"false_positives"`
	Precision    float64 `json:"precision"`
}

type Report struct {
	GeneratedAt   time.Time        `json:"generated_at"`
	Inputs        Inputs           `json:"inputs"`
	Overall       Overall          `json:"overall"`
	PerTag        []TagMetric      `json:"per_tag"`
	DLQ           DLQ              `json:"dlq"`
	Timeline      []TimelineBucket `json:"timeline"`
	Warnings      []string         `json:"warnings,omitempty"`
	LedgerEntries int              `json:"ledger_entries,omitempty"` // для human-заголовка (spec §7)
}

// LoadFindings читает findings.jsonl.
func LoadFindings(path string) ([]checks.Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []checks.Finding
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		var fd checks.Finding
		if err := json.Unmarshal([]byte(s), &fd); err != nil {
			return nil, fmt.Errorf("findings %s line %d: %w", path, line, err)
		}
		out = append(out, fd)
	}
	return out, sc.Err()
}

type ledgerScan struct {
	entries     int
	tagOrders   map[string]map[string]struct{} // тег → множество order_id
	tagTS       map[string][2]time.Time        // тег → (first, last)
	hasTS       map[string]bool
	rawTagCount map[string]int // raw count of ledger rows per tag
}

func (l *ledgerScan) firstTS(tag string) string {
	if !l.hasTS[tag] {
		return ""
	}
	return l.tagTS[tag][0].UTC().Format(time.RFC3339Nano)
}

func (l *ledgerScan) lastTS(tag string) string {
	if !l.hasTS[tag] {
		return ""
	}
	return l.tagTS[tag][1].UTC().Format(time.RFC3339Nano)
}

func scanLedger(path string, warnings *[]string) (*ledgerScan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ls := &ledgerScan{
		tagOrders:   map[string]map[string]struct{}{},
		tagTS:       map[string][2]time.Time{},
		hasTS:       map[string]bool{},
		rawTagCount: map[string]int{},
	}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		var e producer.LedgerEntry
		if err := json.Unmarshal([]byte(s), &e); err != nil {
			return nil, fmt.Errorf("ledger %s line %d: %w", path, line, err)
		}
		ls.entries++
		tag := string(e.Defect)
		if tag == "none" {
			continue
		}
		if _, ok := validDefects[tag]; !ok {
			*warnings = append(*warnings,
				fmt.Sprintf("ledger line %d: unknown defect %q — excluded from per_tag", line, tag))
			continue
		}
		// Increment raw count for the tag (used for invalidjson calculations)
		ls.rawTagCount[tag]++
		if e.OrderID != "" {
			if ls.tagOrders[tag] == nil {
				ls.tagOrders[tag] = map[string]struct{}{}
			}
			ls.tagOrders[tag][e.OrderID] = struct{}{}
		}
		if !ls.hasTS[tag] {
			ls.tagTS[tag] = [2]time.Time{e.Ts, e.Ts}
			ls.hasTS[tag] = true
		} else {
			ts := ls.tagTS[tag]
			if e.Ts.Before(ts[0]) {
				ts[0] = e.Ts
			}
			if e.Ts.After(ts[1]) {
				ts[1] = e.Ts
			}
			ls.tagTS[tag] = ts
		}
	}
	return ls, sc.Err()
}

// falsePositives — findings check'а, чей order_id отсутствует в множестве
// order_id строк ledger данного тега (spec §4); invalidjson —
// max(0, findings−total).
func falsePositives(tag, check string, findingsCount int, findings []checks.Finding, ls *ledgerScan) int {
	if tag == "invalidjson" {
		if n := findingsCount - ls.tagTotalCount(tag); n > 0 {
			return n
		}
		return 0
	}
	fp := 0
	ids := ls.tagOrders[tag]
	for _, f := range findings {
		if f.Check != check {
			continue
		}
		if f.OrderID == "" {
			fp++
			continue
		}
		if _, ok := ids[f.OrderID]; !ok {
			fp++
		}
	}
	return fp
}

func (l *ledgerScan) tagTotalCount(tag string) int {
	// Сырой счёт строк ledger тега — дублируем подсчёт (tagOrders хранит
	// множества; для invalidjson order_id пуст, строки считаются здесь).
	return l.rawTagCount[tag]
}

// BuildReport строит отчёт аудита (spec §4–§7).
// Сходимость с consumer'ом: caught/total — через report.Summary
// (LoadLedger + AddFinding + ComputeCaught).
func BuildReport(ledgerPath, findingsPath string, generatedAt time.Time) (Report, error) {
	rep := Report{
		GeneratedAt: generatedAt,
		Inputs:      Inputs{Ledger: ledgerPath, Findings: findingsPath},
		DLQ:         DLQ{ByReason: map[string]int{}},
	}

	summary := report.NewSummary()
	if err := summary.LoadLedger(ledgerPath); err != nil {
		return rep, fmt.Errorf("ledger: %w", err)
	}
	findings, err := LoadFindings(findingsPath)
	if err != nil {
		return rep, err
	}
	for _, f := range findings {
		summary.AddFinding(f)
		if _, ok := validChecks[f.Check]; !ok {
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("findings: unknown check %q — excluded from metrics", f.Check))
		}
	}
	summary.ComputeCaught(findings)

	ls, err := scanLedger(ledgerPath, &rep.Warnings)
	if err != nil {
		return rep, err
	}
	rep.LedgerEntries = ls.entries

	rep.PerTag = make([]TagMetric, 0, len(tagOrder))
	for _, tag := range tagOrder {
		check := tagToCheck[tag]
		total := summary.TagTotal[tag]
		caught := summary.Caught[tag]
		fnd := summary.ByCheck[check]
		fp := falsePositives(tag, check, fnd, findings, ls)
		rep.PerTag = append(rep.PerTag, TagMetric{
			Tag:       tag,
			Check:     check,
			Total:     total,
			Caught:    caught,
			Recall:    round4(ratio(caught, total)),
			Findings:  fnd,
			FalsePos:  fp,
			Precision: round4(ratio(fnd-fp, fnd)),
			FirstTS:   ls.firstTS(tag),
			LastTS:    ls.lastTS(tag),
		})
	}

	for _, f := range findings {
		if checks.IsSchemaViolation(f.Check) {
			rep.DLQ.Count++
			rep.DLQ.ByReason[f.Check]++
		}
	}

	buckets := map[int64]int{}
	for _, f := range findings {
		buckets[f.Ts.Unix()]++
	}
	rep.Timeline = make([]TimelineBucket, 0, len(buckets))
	for b, c := range buckets {
		rep.Timeline = append(rep.Timeline, TimelineBucket{BucketS: b, Count: c})
	}
	sort.Slice(rep.Timeline, func(i, j int) bool {
		return rep.Timeline[i].BucketS < rep.Timeline[j].BucketS
	})

	var o Overall
	for _, m := range rep.PerTag {
		o.TotalDefects += m.Total
		o.Caught += m.Caught
		o.Findings += m.Findings
		o.FalsePos += m.FalsePos
	}
	o.Recall = round4(ratio(o.Caught, o.TotalDefects))
	o.Precision = round4(ratio(o.Findings-o.FalsePos, o.Findings))
	rep.Overall = o
	return rep, nil
}

func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func round4(x float64) float64 {
	return math.Round(x*10000) / 10000
}
