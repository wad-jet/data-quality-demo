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
