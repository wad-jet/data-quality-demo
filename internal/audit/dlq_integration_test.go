package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dqdemo/internal/audit"
	"dqdemo/internal/checks"
	"dqdemo/internal/consumer"
	"dqdemo/internal/producer"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestDLQIntegration(t *testing.T) {
	// Broker availability check, skip if not.
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
	uniq := fmt.Sprintf("%d", time.Now().UnixNano())
	topic := "dq.it.audit.orders." + uniq
	dlqTopic := topic + ".dlq"

	rates := producer.Rates{Missing: 0.15, Dup: 0.15, TypeDrift: 0.15, OOO: 0.15, Lag: 0.15, InvalidJSON: 0.1}
	gen, err := producer.NewGenerator(42, rates, time.Now)
	if err != nil {
		t.Fatalf("generator: %v", err)
	}
	emitter := producer.NewEmitter([]string{"localhost:9092"}, topic)
	if err := emitter.EnsureTopic(context.Background()); err != nil {
		t.Fatalf("ensure topic: %v", err)
	}

	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	ledger, err := producer.NewLedger(ledgerPath)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}

	// Produce 1000 events.
	for i := 0; i < 1000; i++ {
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

	findingsPath := filepath.Join(dir, "findings.jsonl")
	cfg := consumer.Config{Bootstrap: "localhost:9092", Topic: topic, Group: "dq-it-audit-dlq-" + uniq, DLQTopic: dlqTopic, LagThreshold: 60 * time.Second, StopN: 0, IdleStop: 3 * time.Second}
	cons, err := consumer.New(cfg, findingsPath, ledgerPath)
	if err != nil {
		t.Fatalf("consumer new: %v", err)
	}
	ctxRun, cancelRun := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelRun()
	if err = cons.Run(ctxRun); err != nil {
		t.Fatalf("consumer run: %v", err)
	}
	// Allow broker metadata propagation before consuming DLQ
	time.Sleep(2 * time.Second)

	// Compute expected DLQ stats from the findings produced.
	data, err := os.ReadFile(findingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	var expectedCount int
	expectedByReason := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f checks.Finding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("unmarshal finding: %v", err)
		}
		if checks.IsSchemaViolation(f.Check) {
			expectedCount++
			expectedByReason[f.Check]++
		}
	}
	if expectedCount == 0 {
		t.Fatalf("expected non-zero DLQ count, got 0")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, warns, err := audit.ConsumeDLQ(ctx, "localhost:9092", dlqTopic)
	if err != nil {
		t.Fatalf("ConsumeDLQ error: %v", err)
	}
	if got.Count != expectedCount {
		t.Fatalf("DLQ count mismatch: got %d, want %d", got.Count, expectedCount)
	}
	if !reflect.DeepEqual(got.ByReason, expectedByReason) {
		t.Fatalf("DLQ byReason mismatch: got %#v, want %#v", got.ByReason, expectedByReason)
	}
	if len(warns) != 0 {
		t.Fatalf("expected no warnings, got %d: %v", len(warns), warns)
	}

	rep, err := audit.BuildReport(ledgerPath, findingsPath, time.Now())
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	if rep.DLQ.Count != expectedCount {
		t.Fatalf("Report DLQ count mismatch: %d vs %d", rep.DLQ.Count, expectedCount)
	}
	if !reflect.DeepEqual(rep.DLQ.ByReason, expectedByReason) {
		t.Fatalf("Report DLQ byReason mismatch: %#v vs %#v", rep.DLQ.ByReason, expectedByReason)
	}
	if w := audit.CheckDLQTopic(rep.DLQ, got); w != "" {
		t.Fatalf("CheckDLQTopic warning unexpected: %s", w)
	}

	ctxNeg, cancelNeg := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelNeg()
	_, _, err = audit.ConsumeDLQ(ctxNeg, "localhost:9092", "dq.it.audit.orders.does-not-exist."+uniq)
	if err == nil {
		t.Fatalf("expected error for unknown DLQ topic, got nil")
	}
}
