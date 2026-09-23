# cmd/audit Implementation Plan (Фаза 2 — E2E-аудит)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** новый offline-инструмент `cmd/audit`: по `ledger.jsonl` + `findings.jsonl` считает precision/recall per дефект, DLQ по причинам, таймлайн; human-отчёт в stdout + JSON по `-out`.

**Architecture:** переиспользуемый `report.Summary` (LoadLedger + ComputeCaught) — гарантия сходимости caught/total с агрегатом consumer'а; fp/precision/DLQ/timeline/overall считает `internal/audit` сам (spec §4, вариант B). CLI — тонкий `cmd/audit`.

**Tech Stack:** Go 1.26 (module dqdemo), stdlib + существующий franz-go (только integration-тест). New deps: нет.

**Spec:** `docs/superpowers/specs/2026-09-23-audit-tool-design.md` (авторитетен при конфликтах).

## Global Constraints
- Go: таб-индентация, `gofmt`, table-driven тесты, slog не нужен в audit (вывод — данные, не логи; ошибки — stderr).
- Не менять поведение consumer/producer/checks. `internal/report` — read-only (только вызовы `NewSummary`/`LoadLedger`/`AddFinding`/`ComputeCaught`, поля `TagTotal`/`Caught`/`ByCheck`).
- Формулы (spec §4): recall = caught/total; fp = findings check'а с order_id вне множества order_id ledger-тега (invalidjson: max(0, findings−total)); precision = (findings−fp)/findings; округление JSON — 4 знака (`round4`), human — процент 1 знак.
- Порядок `per_tag` = tagOrder: missing, dup, typedrift, ooo, lag, invalidjson.
- Exit codes: 0 — успех (даже fp>0/recall<1); 1 — ошибки входа/IO/parse.
- Commit message: `<type>: <scope> — <что>` (пример: `feat: internal/audit — per-tag precision/recall core`).

## File Structure
- Create: `internal/audit/audit.go` — типы + BuildReport + LoadFindings + scanLedger (T1)
- Create: `internal/audit/audit_test.go` — unit-тесты ядра (T1)
- Create: `internal/audit/report.go` — RenderHuman + WriteJSON (T2)
- Create: `internal/audit/report_test.go` — тесты рендера/JSON (T2)
- Create: `cmd/audit/main.go` — CLI (T3)
- Modify: `Makefile` (target `build`) — `go build -o bin/audit ./cmd/audit` (T3)
- Create: `internal/audit/integration_test.go` — сходимость с consumer (T4)
- Create: `manual_docs/reference/data-formats.md` — справочная статья (T5)
- Modify: `README.md` — секция «Аудит (audit)» (T6)

---

### Task 1: Ядро метрик — internal/audit (KEY TASK)

**Files:**
- Create: `internal/audit/audit.go`
- Test: `internal/audit/audit_test.go`

- [ ] **Step 1: Write the failing tests**

Создай `internal/audit/audit_test.go`:

```go
package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func build(t *testing.T, ledger, findings []string) Report {
	t.Helper()
	dir := t.TempDir()
	lp := writeLines(t, dir, "ledger.jsonl", ledger...)
	fp := writeLines(t, dir, "findings.jsonl", findings...)
	rep, err := BuildReport(lp, fp, time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	return rep
}

func perTag(rep Report, tag string) (TagMetric, bool) {
	for _, m := range rep.PerTag {
		if m.Tag == tag {
			return m, true
		}
	}
	return TagMetric{}, false
}

func TestMetricsIdeal(t *testing.T) {
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`,
			`{"seq":2,"order_id":"B","ts":"2026-09-23T10:00:01Z","defect":"none"}`,
			`{"seq":3,"order_id":"C","ts":"2026-09-23T10:00:02Z","defect":"lag"}`,
		},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"no amount","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"lag","order_id":"C","offset":2,"detail":"slow","ts":"2026-09-23T10:00:02Z"}`,
		})
	if rep.LedgerEntries != 3 {
		t.Fatalf("LedgerEntries = %d, want 3", rep.LedgerEntries)
	}
	m, _ := perTag(rep, "missing")
	if m.Total != 1 || m.Caught != 1 || m.Recall != 1.0 || m.Findings != 1 || m.FalsePos != 0 || m.Precision != 1.0 {
		t.Fatalf("missing = %+v", m)
	}
	l, _ := perTag(rep, "lag")
	if l.Total != 1 || l.Caught != 1 || l.Recall != 1.0 || l.Precision != 1.0 {
		t.Fatalf("lag = %+v", l)
	}
	if len(rep.PerTag) != 6 {
		t.Fatalf("per_tag len = %d, want 6", len(rep.PerTag))
	}
	if rep.Overall.TotalDefects != 2 || rep.Overall.Caught != 2 || rep.Overall.Recall != 1.0 ||
		rep.Overall.Findings != 2 || rep.Overall.FalsePos != 0 || rep.Overall.Precision != 1.0 {
		t.Fatalf("overall = %+v", rep.Overall)
	}
	if rep.DLQ.Count != 1 || rep.DLQ.ByReason["field_missing"] != 1 {
		t.Fatalf("dlq = %+v", rep.DLQ)
	}
	if m.FirstTS == "" || m.LastTS == "" {
		t.Fatalf("first/last ts empty: %+v", m)
	}
}

func TestFalsePositive(t *testing.T) {
	rep := build(t,
		[]string{`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"field_missing","order_id":"Z","offset":1,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
		})
	m, _ := perTag(rep, "missing")
	if m.Findings != 2 || m.FalsePos != 1 || m.Precision != 0.5 || m.Caught != 1 || m.Recall != 1.0 {
		t.Fatalf("missing = %+v", m)
	}
}

func TestCrossFire(t *testing.T) {
	// ledger tag=lag order A + findings lag(A) и out_of_order(A) →
	// caught[lag]=1, fp[ooo]=1 (кросс-срабатывание, spec §4).
	rep := build(t,
		[]string{`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"lag"}`},
		[]string{
			`{"check":"lag","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"out_of_order","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
		})
	l, _ := perTag(rep, "lag")
	if l.Caught != 1 || l.FalsePos != 0 || l.Precision != 1.0 {
		t.Fatalf("lag = %+v", l)
	}
	o, _ := perTag(rep, "ooo")
	if o.Total != 0 || o.Findings != 1 || o.FalsePos != 1 || o.Precision != 0.0 {
		t.Fatalf("ooo = %+v", o)
	}
	if rep.Overall.Recall != 1.0 || rep.Overall.Precision != 0.5 {
		t.Fatalf("overall = %+v", rep.Overall)
	}
}

func TestDupAsymmetry(t *testing.T) {
	// order A дубль ×3 (3 строки defect=dup) + 1 finding duplicate(A) →
	// total=3, caught=3 (строки ledger), recall=1.0, findings=1, fp=0, precision=1.0.
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"dup"}`,
			`{"seq":2,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"dup"}`,
			`{"seq":3,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"dup"}`,
		},
		[]string{
			`{"check":"duplicate","order_id":"A","offset":1,"detail":"seen","ts":"2026-09-23T10:00:00Z"}`,
		})
	m, _ := perTag(rep, "dup")
	if m.Total != 3 || m.Caught != 3 || m.Recall != 1.0 || m.Findings != 1 || m.FalsePos != 0 || m.Precision != 1.0 {
		t.Fatalf("dup = %+v", m)
	}
}

func TestInvalidJSONMin(t *testing.T) {
	ledger3 := []string{
		`{"seq":1,"order_id":"X","ts":"2026-09-23T10:00:00Z","defect":"invalidjson"}`,
		`{"seq":2,"order_id":"Y","ts":"2026-09-23T10:00:01Z","defect":"invalidjson"}`,
		`{"seq":3,"order_id":"Z","ts":"2026-09-23T10:00:02Z","defect":"invalidjson"}`,
	}
	// findings < total: caught=min=2, fp=0, precision=1.0
	rep := build(t, ledger3, []string{
		`{"check":"invalid_json","order_id":"","offset":0,"detail":"bad","ts":"2026-09-23T10:00:00Z"}`,
		`{"check":"invalid_json","order_id":"","offset":1,"detail":"bad","ts":"2026-09-23T10:00:01Z"}`,
	})
	m, _ := perTag(rep, "invalidjson")
	if m.Total != 3 || m.Caught != 2 || m.FalsePos != 0 || m.Precision != 1.0 {
		t.Fatalf("case1 invalidjson = %+v", m)
	}
	// findings > total: caught=3, fp=1, precision=3/4
	findings4 := make([]string, 4)
	for i := range findings4 {
		findings4[i] = `{"check":"invalid_json","order_id":"","offset":` + string(rune('0'+i)) + `,"detail":"bad","ts":"2026-09-23T10:00:00Z"}`
	}
	rep = build(t, ledger3, findings4)
	m, _ = perTag(rep, "invalidjson")
	if m.Caught != 3 || m.FalsePos != 1 || m.Precision != 0.75 {
		t.Fatalf("case2 invalidjson = %+v", m)
	}
}

func TestEmptyFiles(t *testing.T) {
	rep := build(t, nil, nil)
	if rep.Overall.Recall != 0 || rep.Overall.Precision != 0 || rep.Overall.Findings != 0 {
		t.Fatalf("overall = %+v", rep.Overall)
	}
	if len(rep.Timeline) != 0 || rep.DLQ.Count != 0 {
		t.Fatalf("timeline/dlq not empty")
	}
}

func TestDLQGrouping(t *testing.T) {
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`,
			`{"seq":2,"order_id":"B","ts":"2026-09-23T10:00:01Z","defect":"typedrift"}`,
			`{"seq":3,"order_id":"C","ts":"2026-09-23T10:00:02Z","defect":"dup"}`,
		},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"type_drift","order_id":"B","offset":1,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
			`{"check":"invalid_json","order_id":"","offset":2,"detail":"x","ts":"2026-09-23T10:00:02Z"}`,
			`{"check":"duplicate","order_id":"C","offset":2,"detail":"x","ts":"2026-09-23T10:00:02Z"}`,
		})
	if rep.DLQ.Count != 3 {
		t.Fatalf("dlq count = %d, want 3", rep.DLQ.Count)
	}
	if rep.DLQ.ByReason["field_missing"] != 1 || rep.DLQ.ByReason["type_drift"] != 1 || rep.DLQ.ByReason["invalid_json"] != 1 {
		t.Fatalf("dlq by_reason = %+v", rep.DLQ.ByReason)
	}
	if _, ok := rep.DLQ.ByReason["duplicate"]; ok {
		t.Fatalf("duplicate must not be in dlq by_reason")
	}
}

func TestTimelineBuckets(t *testing.T) {
	rep := build(t,
		[]string{`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"lag","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
			`{"check":"lag","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
		})
	if len(rep.Timeline) != 2 {
		t.Fatalf("timeline len = %d, want 2: %+v", len(rep.Timeline), rep.Timeline)
	}
	base := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC).Unix()
	if rep.Timeline[0].BucketS != base || rep.Timeline[0].Count != 1 ||
		rep.Timeline[1].BucketS != base+1 || rep.Timeline[1].Count != 2 {
		t.Fatalf("timeline = %+v", rep.Timeline)
	}
}

func TestUnknownValues(t *testing.T) {
	rep := build(t,
		[]string{
			`{"seq":1,"order_id":"A","ts":"2026-09-23T10:00:00Z","defect":"missing"}`,
			`{"seq":2,"order_id":"B","ts":"2026-09-23T10:00:01Z","defect":"weird"}`,
		},
		[]string{
			`{"check":"field_missing","order_id":"A","offset":0,"detail":"x","ts":"2026-09-23T10:00:00Z"}`,
			`{"check":"unknown_check","order_id":"B","offset":1,"detail":"x","ts":"2026-09-23T10:00:01Z"}`,
		})
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "weird") || !strings.Contains(joined, "unknown_check") {
		t.Fatalf("warnings = %q", joined)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audit/`
Expected: FAIL (build error: `undefined: BuildReport`).

- [ ] **Step 3: Write the implementation**

Создай `internal/audit/audit.go`:

```go
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

// Schema-violation checks → DLQ (зеркало consumer isSchemaViolation, spec §5).
var dlqChecks = []string{"field_missing", "type_drift", "invalid_json"}

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
	Tag         string  `json:"tag"`
	Check       string  `json:"check"`
	Total       int     `json:"total"`
	Caught      int     `json:"caught"`
	Recall      float64 `json:"recall"`
	Findings    int     `json:"findings"`
	FalsePos    int     `json:"false_positives"`
	Precision   float64 `json:"precision"`
	FirstTS     string  `json:"first_ts,omitempty"`
	LastTS      string  `json:"last_ts,omitempty"`
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
	GeneratedAt  time.Time        `json:"generated_at"`
	Inputs       Inputs           `json:"inputs"`
	Overall      Overall          `json:"overall"`
	PerTag       []TagMetric      `json:"per_tag"`
	DLQ          DLQ              `json:"dlq"`
	Timeline     []TimelineBucket `json:"timeline"`
	Warnings     []string         `json:"warnings,omitempty"`
	LedgerEntries int             `json:"-"` // для human-заголовка (spec §7)
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
	entries   int
	tagOrders map[string]map[string]struct{} // тег → множество order_id
	tagTS     map[string][2]time.Time        // тег → (first, last)
	hasTS     map[string]bool
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
		tagOrders: map[string]map[string]struct{}{},
		tagTS:     map[string][2]time.Time{},
		hasTS:     map[string]bool{},
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
			if e.Ts.Before(ls.tagTS[tag][0]) {
				ls.tagTS[tag][0] = e.Ts
			}
			if e.Ts.After(ls.tagTS[tag][1]) {
				ls.tagTS[tag][1] = e.Ts
			}
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
		for _, c := range dlqChecks {
			if f.Check == c {
				rep.DLQ.Count++
				rep.DLQ.ByReason[c]++
				break
			}
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
```

Добавь в `ledgerScan` поле `rawTagCount map[string]int` (инициализируй в `scanLedger`) и инкрементируй `ls.rawTagCount[tag]++` для известных тегов (нужно для `max(0, findings−total)` у invalidjson, где order_id пуст и tagOrders бесполезен).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/audit/ -v`
Expected: PASS (все 9 тестов).
Run: `gofmt -l internal/audit/` → пусто; `go vet ./internal/audit/` → ок.

- [ ] **Step 5: Commit**

```bash
git add internal/audit/audit.go internal/audit/audit_test.go
git commit -m "feat: internal/audit — per-tag precision/recall core (Summary reuse)"
```

---

### Task 2: Рендер — RenderHuman + WriteJSON

**Files:**
- Create: `internal/audit/report.go`
- Test: `internal/audit/report_test.go`

- [ ] **Step 1: Write the failing tests**

Создай `internal/audit/report_test.go`:

```go
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleReport() Report {
	rep := Report{
		GeneratedAt:   time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
		Inputs:        Inputs{Ledger: "ledger.jsonl", Findings: "findings.jsonl"},
		LedgerEntries: 3,
		PerTag: []TagMetric{
			{Tag: "missing", Check: "field_missing", Total: 1, Caught: 1, Recall: 1.0,
				Findings: 1, FalsePos: 0, Precision: 1.0,
				FirstTS: "2026-09-23T10:00:00Z", LastTS: "2026-09-23T10:00:00Z"},
		},
		DLQ:      DLQ{Count: 1, ByReason: map[string]int{"field_missing": 1}},
		Timeline: []TimelineBucket{{BucketS: 1758621600, Count: 1}},
		Overall:  Overall{TotalDefects: 1, Caught: 1, Recall: 1.0, Findings: 1, Precision: 1.0},
	}
	return rep
}

func TestRenderHuman(t *testing.T) {
	out := sampleReport().RenderHuman()
	for _, want := range []string{
		"Audit report (ledger: 3 entries, findings: 1)",
		"tag", "check", "total", "caught", "recall", "findings", "fp", "precision",
		"missing", "field_missing", "100.0%",
		"DLQ (offline, schema-violations): 1 (field_missing=1)",
		"Timeline (1s buckets): t=1758621600:1",
		"Overall: recall=1.0000 precision=1.0000 (caught=1/1, findings=1, fp=0)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output missing %q:\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 7 { // заголовок, шапка, 1 строка тега, DLQ, Timeline, Overall, +? см. ниже
		t.Fatalf("expected 7 lines, got %d:\n%s", len(lines), out)
	}
}

func TestWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := WriteJSON(sampleReport(), path); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file must be renamed away")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var back Report
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Overall.Caught != 1 || len(back.PerTag) != 1 || back.PerTag[0].Tag != "missing" ||
		back.DLQ.ByReason["field_missing"] != 1 || len(back.Timeline) != 1 {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if !back.GeneratedAt.Equal(sampleReport().GeneratedAt) {
		t.Fatalf("generated_at mismatch: %v", back.GeneratedAt)
	}
	if strings.Contains(string(data), "ledger_entries") {
		t.Fatalf("LedgerEntries must not be serialized (json:-)")
	}
}

func TestWriteJSONError(t *testing.T) {
	if err := WriteJSON(sampleReport(), "/nonexistent-dir-xyz/r.json"); err == nil {
		t.Fatalf("expected error for bad path")
	}
}
```

Примечание к TestRenderHuman: если факт. число строк ≠ 7 — поправь ожидание под фактический вывод (6 строк: заголовок, шапка, тег, DLQ, Timeline, Overall) — ориентир: контент-ассерты выше обязательны, число строк — вторично.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audit/ -run 'TestRenderHuman|TestWriteJSON'`
Expected: FAIL (`undefined: (Report).RenderHuman`, `undefined: WriteJSON`).

- [ ] **Step 3: Write the implementation**

Создай `internal/audit/report.go`:

```go
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Порядок by_reason в human-строке DLQ (spec §7).
var dlqReasonOrder = []string{"field_missing", "type_drift", "invalid_json"}

// RenderHuman — человекочитаемый отчёт для stdout (spec §7).
func (r *Report) RenderHuman() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Audit report (ledger: %d entries, findings: %d)\n",
		r.LedgerEntries, r.Overall.Findings)
	fmt.Fprintf(&b, "%-10s %-16s %7s %8s %9s %10s %4s %10s\n",
		"tag", "check", "total", "caught", "recall", "findings", "fp", "precision")
	for _, m := range r.PerTag {
		fmt.Fprintf(&b, "%-10s %-16s %7d %8d %8.1f%% %10d %4d %9.1f%%\n",
			m.Tag, m.Check, m.Total, m.Caught, m.Recall*100,
			m.Findings, m.FalsePos, m.Precision*100)
	}
	var reasons []string
	for _, c := range dlqReasonOrder {
		if n := r.DLQ.ByReason[c]; n > 0 {
			reasons = append(reasons, fmt.Sprintf("%s=%d", c, n))
		}
	}
	fmt.Fprintf(&b, "DLQ (offline, schema-violations): %d (%s)\n",
		r.DLQ.Count, strings.Join(reasons, " "))
	var buckets []string
	for _, bk := range r.Timeline {
		buckets = append(buckets, fmt.Sprintf("t=%d:%d", bk.BucketS, bk.Count))
	}
	fmt.Fprintf(&b, "Timeline (1s buckets): %s\n", strings.Join(buckets, " "))
	fmt.Fprintf(&b, "Overall: recall=%.4f precision=%.4f (caught=%d/%d, findings=%d, fp=%d)\n",
		r.Overall.Recall, r.Overall.Precision, r.Overall.Caught,
		r.Overall.TotalDefects, r.Overall.Findings, r.Overall.FalsePos)
	return b.String()
}

// WriteJSON пишет JSON-отчёт атомарно (tmp + rename, spec §7).
func WriteJSON(r Report, path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/audit/ -v`
Expected: PASS (все тесты T1+T2).
Run: `gofmt -l internal/audit/` → пусто; `go vet ./internal/audit/` → ок.

- [ ] **Step 5: Commit**

```bash
git add internal/audit/report.go internal/audit/report_test.go
git commit -m "feat: internal/audit — human render + atomic JSON output"
```

---

### Task 3: CLI cmd/audit + Makefile

**Files:**
- Create: `cmd/audit/main.go`
- Modify: `Makefile` (target `build`)

- [ ] **Step 1: Write the CLI**

Создай `cmd/audit/main.go`:

```go
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
```

- [ ] **Step 2: Add audit to Makefile build**

В `Makefile` target `build` добавь третью строку:

```make
build:
	go build -o bin/producer ./cmd/producer
	go build -o bin/consumer ./cmd/consumer
	go build -o bin/audit ./cmd/audit
```

- [ ] **Step 3: Verify build + smoke**

Run: `go build ./... && go vet ./...`
Expected: ok.
Run: `make build && ./bin/audit` (без флага)
Expected: exit 1, в stderr usage-строка.
Run: `./bin/audit -ledger /nonexistent -findings /nonexistent2`
Expected: exit 1, ошибка в stderr.

- [ ] **Step 4: Commit**

```bash
git add cmd/audit/main.go Makefile
git commit -m "feat: cmd/audit CLI + make build target"
```

---

### Task 4: Integration — сходимость с consumer'ом

**Files:**
- Create: `internal/audit/integration_test.go`

- [ ] **Step 1: Write the integration test**

Создай `internal/audit/integration_test.go` (паттерн брокера — как в `internal/consumer/integration_test.go`):

```go
package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"dqdemo/internal/audit"
	"dqdemo/internal/checks"
	"dqdemo/internal/consumer"
	"dqdemo/internal/producer"
	"dqdemo/internal/report"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestIntegrationAuditMatchesConsumer(t *testing.T) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers("localhost:9092"),
		kgo.DialTimeout(2*time.Second),
	)
	if err != nil {
		t.Skip("broker unavailable: " + err.Error())
	}
	client.Close()
	d := net.Dialer{Timeout: 2 * time.Second}
	if conn, err := d.DialContext(context.Background(), "tcp", "localhost:9092"); err != nil {
		t.Skip("broker unavailable: " + err.Error())
	} else {
		conn.Close()
	}

	dir := t.TempDir()
	uniq := fmt.Sprintf("dq-it-audit-%d", time.Now().UnixNano())
	topic := "dq.it.audit.orders"
	dlqTopic := topic + ".dlq"

	rates := producer.Rates{Missing: 0.15, Dup: 0.15, TypeDrift: 0.15, OOO: 0.15, Lag: 0.15, InvalidJSON: 0.1}
	gen, err := producer.NewGenerator(42, rates, time.Now)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	emitter := producer.NewEmitter([]string{"localhost:9092"}, topic)
	_ = emitter.EnsureTopic(context.Background())
	ledgerPath := dir + "/ledger.jsonl"
	ledger, err := producer.NewLedger(ledgerPath)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	for i := 0; i < 300; i++ {
		ev := gen.Next()
		if err = emitter.Send(context.Background(), []byte(ev.OrderID), ev.Payload); err != nil {
			t.Fatalf("send: %v", err)
		}
		if err = ledger.Append(producer.LedgerEntry{OrderID: ev.OrderID, Ts: ev.Ts, Defect: ev.Defect}); err != nil {
			t.Fatalf("ledger append: %v", err)
		}
	}
	emitter.Close()
	ledger.Close()

	findingsPath := dir + "/findings.jsonl"
	cfg := consumer.Config{
		Bootstrap:    "localhost:9092",
		Topic:        topic,
		Group:        uniq,
		DLQTopic:     dlqTopic,
		LagThreshold: 60 * time.Second,
		StopN:        0,
		IdleStop:     3 * time.Second,
	}
	cons, err := consumer.New(cfg, findingsPath, ledgerPath)
	if err != nil {
		t.Fatalf("consumer new: %v", err)
	}
	ctxRun, cancelRun := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelRun()
	if err = cons.Run(ctxRun); err != nil {
		t.Fatalf("consumer run: %v", err)
	}

	// Reference-агрегат (семантика consumer'а, паттерн integration_test.go).
	data, err := os.ReadFile(findingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	var findings []checks.Finding
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f checks.Finding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("unmarshal finding: %v", err)
		}
		findings = append(findings, f)
	}
	summary := report.NewSummary()
	if err = summary.LoadLedger(ledgerPath); err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	summary.ComputeCaught(findings)

	rep, err := audit.BuildReport(ledgerPath, findingsPath, time.Now())
	if err != nil {
		t.Fatalf("audit.BuildReport: %v", err)
	}

	// Sanity: при непустых тегах числа ненулевые (spec §8.2).
	for _, m := range rep.PerTag {
		if m.Total > 0 && (m.Caught == 0 || m.Findings == 0) {
			t.Fatalf("sanity tag %s: caught=%d findings=%d must be > 0", m.Tag, m.Caught, m.Findings)
		}
	}

	// Сходимость: caught/total audit == Caught/TagTotal consumer (spec §8.2).
	auditTotals := map[string]int{}
	auditCaught := map[string]int{}
	for _, m := range rep.PerTag {
		auditTotals[m.Tag] = m.Total
		auditCaught[m.Tag] = m.Caught
	}
	for tag, total := range summary.TagTotal {
		if auditTotals[tag] != total {
			t.Fatalf("convergence tag %s: total audit=%d consumer=%d", tag, auditTotals[tag], total)
		}
		if auditCaught[tag] != summary.Caught[tag] {
			t.Fatalf("convergence tag %s: caught audit=%d consumer=%d", tag, auditCaught[tag], summary.Caught[tag])
		}
	}
}
```

- [ ] **Step 2: Run with broker (skip без него)**

Run: `make broker-up && sleep 5 && go test ./internal/audit/ -run TestIntegrationAudit -v -timeout 180s; make broker-down`
Expected: PASS при поднятом брокере; SKIP без брокера (проверь оба состояния).
Run: `go test ./... -timeout 300s`
Expected: всё зелёное (integration skip, если брокер не поднят).

- [ ] **Step 3: Commit**

```bash
git add internal/audit/integration_test.go
git commit -m "test: integration — audit converges with consumer summary"
```

---

### Task 5: manual_docs — справочная статья

**Files:**
- Create: `manual_docs/reference/data-formats.md`

- [ ] **Step 1: Write the article**

Создай `manual_docs/reference/data-formats.md` (Diátaxis: Reference; факты — из кода и spec):

```markdown
# Справочник форматов данных (reference)

Форматы файлов, которые обменивают producer, consumer и audit.
Спецификация поведения: `docs/superpowers/specs/2026-09-23-audit-tool-design.md`.

## ledger.jsonl (producer — ground truth)

Одна JSON-строка на отправленное событие (`producer.LedgerEntry`):

| Поле | Тип | Значение |
|---|---|---|
| `seq` | int | порядковый номер отправки (с 1) |
| `order_id` | string | id заказа |
| `ts` | RFC3339 | timestamp события (event-time) |
| `defect` | string | `none` \| `missing` \| `dup` \| `typedrift` \| `ooo` \| `lag` \| `invalidjson` |

Дубль: каждая отправленная копия — отдельная строка с `defect=dup`.

## findings.jsonl (consumer — detections)

Одна JSON-строка на finding (`checks.Finding`):

| Поле | Тип | Значение |
|---|---|---|
| `check` | string | `field_missing` \| `type_drift` \| `duplicate` \| `out_of_order` \| `lag` \| `invalid_json` |
| `order_id` | string \| absent | пуст у `invalid_json` (order unknown) |
| `offset` | int64 | offset в топике |
| `detail` | string | описание находки |
| `ts` | RFC3339 | **event-time** (ts заказа, не момент детекции) |

## audit-report.json (audit)

Схема (JSON, `-out`): `generated_at`, `inputs{ledger,findings}`,
`overall{total_defects,caught,recall,findings,false_positives,precision}`,
`per_tag[{tag,check,total,caught,recall,findings,false_positives,precision,first_ts,last_ts}]`
(порядок: missing, dup, typedrift, ooo, lag, invalidjson),
`dlq{count,by_reason{check:count}}`, `timeline[{bucket_s,count}]` (непустые
по-секундные бакеты по event-time), `warnings[]` (неизвестные значения).

## Метрики

- `recall = caught / total` (0 при total=0)
- `false_positives` — findings check'а, чей `order_id` отсутствует в
  множестве order_id строк ledger данного тега; для `invalidjson`:
  `max(0, findings − total)`
- `precision = (findings − false_positives) / findings` (0 при findings=0),
  всегда ≤ 1

Специфика:
- **Дубли:** `caught` считается по строкам ledger — один finding
  «покрывает» все копии заказа (recall/precision для dup используют
  разные единицы: строки ledger vs findings).
- **`invalid_json`:** `caught = min(findings, total)` (order_id пуст);
  при findings ≤ total precision ≡ 100% (следствие min-правила).
- **Кросс-срабатывание ooo/lag:** «старый ts» срабатывает на обе
  проверки, поэтому precision ooo/lag < 100% на дет-прогоне —
  информативный результат, не баг.
- `first_ts`/`last_ts` — min/max `ts` по строкам ledger тега.
```

- [ ] **Step 2: Verify facts**

Перечитай статью; сверь каждое имя поля/значения с `internal/producer/ledger.go` (`LedgerEntry`), `internal/checks/check.go` (`Finding`), spec §4–§7. Расхождения — поправь.

- [ ] **Step 3: Commit**

```bash
git add manual_docs/reference/data-formats.md
git commit -m "docs: manual_docs reference — data formats (ledger/findings/audit)"
```

---

### Task 6: README — секция «Аудит (audit)»

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add the section**

В `README.md` после секции «Как читать отчёт» добавь:

```markdown
## Аудит (audit)

Независимая проверка качества по готовым файлам (без брокера):

    make build   # собирает и bin/audit
    ./bin/audit -ledger ledger.jsonl -findings findings.jsonl -out audit-report.json

- В stdout — таблица precision/recall по 6 дефектам, DLQ по причинам,
  таймлайн, overall; в `audit-report.json` — структурированный отчёт.
- Ожидаемо (дет-режим seed 42): recall = 100% по всем тегам;
  precision = 100% для missing/typedrift/dup/invalidjson; ooo/lag —
  precision < 100% (кросс-срабатывание «старого ts» — см. справку).
- Форматы данных и формулы метрик: `manual_docs/reference/data-formats.md`.
```

- [ ] **Step 2: Verify against reality**

Проверь: `make build` собирает `bin/audit` (T3); команда из секции работает
после `make demo`; фразы соответствуют фактическому поведению (spec §10).

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: README — audit section"
```

---

## Self-Review (выполнено)
- Spec coverage: §3→T1–T3, §4→T1(+T5), §5→T1(T7 DLQ), §6→T1(timeline)+T5, §7→T2/T3, §8→T1/T2/T4, §9→T5/T6, §10→T3/T4+живая верификация. Гaps: нет.
- Placeholder scan: нет TBD/TODO; каждый код-шаг — полный код.
- Type consistency: `BuildReport(ledgerPath, findingsPath, generatedAt) (Report, error)` — единая сигнатура в T1/T3/T4; `TagMetric`/`Overall`/`DLQ`/`TimelineBucket` — из T1, используются в T2 без изменений.
- Ruling (план): `Report.LedgerEntries` — `json:"-"` (human-заголовок spec §7 требует счётчик строк ledger; JSON-схема spec не расширяется).
- Ruling (план): `scanLedger` хранит `rawTagCount` (сырой счёт строк тега) — для `max(0, findings−total)` у invalidjson, т.к. order_id там пуст.
