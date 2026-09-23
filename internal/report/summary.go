package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"dqdemo/internal/checks"
	
)

// Summary агрегирует findings, DLQ-статистику и (опционально) ledger
// producer'а для сводки caught/total (spec §6).
// ledgerLine — read‑only представление строки ledger (report не зависит от producer/kgo).
type ledgerLine struct {
    OrderID string `json:"order_id"`
    Defect  string `json:"defect"`
}

type Summary struct {
	Total     int
	ByCheck   map[string]int
	DLQ       int
	DLQErrors int
	Caught    map[string]int // тег ledger → caught
	TagTotal  map[string]int // тег ledger → всего

	ledgerLoaded bool
	tagOrders    map[string][]string // тег ledger → order_id по записям (с дублями)
}

func NewSummary() *Summary {
	return &Summary{
		ByCheck:   make(map[string]int),
		Caught:    make(map[string]int),
		TagTotal:  make(map[string]int),
		tagOrders: make(map[string][]string),
	}
}

func (s *Summary) AddFinding(f checks.Finding) {
	s.Total++
	s.ByCheck[f.Check]++
}

func (s *Summary) AddDLQ()      { s.DLQ++ }
func (s *Summary) AddDLQError() { s.DLQErrors++ }

// tagToCheck — сопоставление тегов ledger → типов finding (spec §6).
var tagToCheck = map[string]string{
	"missing":     "field_missing",
	"dup":         "duplicate",
	"typedrift":   "type_drift",
	"ooo":         "out_of_order",
	"lag":         "lag",
	"invalidjson": "invalid_json",
}

var tagOrder = []string{"missing", "dup", "typedrift", "ooo", "lag", "invalidjson"}

var checksOrder = []string{"field_missing", "type_drift", "duplicate", "out_of_order", "lag", "invalid_json"}

// LoadLedger читает JSONL ledger producer'а и строит TagTotal
// и наборы order_id по тегам дефектов.
func (s *Summary) LoadLedger(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e ledgerLine
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return fmt.Errorf("ledger %s: %w", path, err)
		}
		tag := e.Defect
		if tag == "none" {
			continue
		}
		s.TagTotal[tag]++
		if e.OrderID != "" {
			s.tagOrders[tag] = append(s.tagOrders[tag], e.OrderID)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	s.ledgerLoaded = true
	return nil
}

// ComputeCaught считает caught по тегам ledger: для тегов с order_id —
// количество записей ledger тега, для которых есть finding
// соответствующего типа с тем же OrderID; invalidjson (order_id пуст) —
// min(количество findings invalid_json, total invalidjson).
func (s *Summary) ComputeCaught(findings []checks.Finding) {
	if !s.ledgerLoaded {
		return
	}
	ordersByCheck := make(map[string]map[string]struct{})
	countByCheck := make(map[string]int)
	for _, f := range findings {
		countByCheck[f.Check]++
		if f.OrderID != "" {
			if ordersByCheck[f.Check] == nil {
				ordersByCheck[f.Check] = make(map[string]struct{})
			}
			ordersByCheck[f.Check][f.OrderID] = struct{}{}
		}
	}
	for _, tag := range tagOrder {
		if tag == "invalidjson" {
			n := countByCheck["invalid_json"]
			if total := s.TagTotal[tag]; n > total {
				n = total
			}
			s.Caught[tag] = n
			continue
		}
		n := 0
		for _, oid := range s.tagOrders[tag] {
			if _, ok := ordersByCheck[tagToCheck[tag]][oid]; ok {
				n++
			}
		}
		s.Caught[tag] = n
	}
}

// Render строит итоговую сводку в stdout (формат — spec §6).
// Строка Caught/total выводится только если ledger загружен.
func (s *Summary) Render() string {
	var b strings.Builder
	var parts []string
	for _, c := range checksOrder {
		parts = append(parts, fmt.Sprintf("%s=%d", c, s.ByCheck[c]))
	}
	fmt.Fprintf(&b, "Findings: total=%d | %s\n", s.Total, strings.Join(parts, " "))
	fmt.Fprintf(&b, "DLQ: %d (dlq_errors=%d)\n", s.DLQ, s.DLQErrors)
	if s.ledgerLoaded {
		var tp []string
		for _, tag := range tagOrder {
			tp = append(tp, fmt.Sprintf("%s=%d/%d", tag, s.Caught[tag], s.TagTotal[tag]))
		}
		fmt.Fprintf(&b, "Caught/total (vs ledger): %s\n", strings.Join(tp, " "))
	}
	return b.String()
}
