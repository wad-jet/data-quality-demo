package consumer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"dqdemo/internal/checks"
	"dqdemo/internal/consumer"
	"dqdemo/internal/producer"
	"dqdemo/internal/report"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestIntegrationConsumer(t *testing.T) {
	// quick broker availability check
	client, err := kgo.NewClient(
		kgo.SeedBrokers("localhost:9092"),
		kgo.DialTimeout(2*time.Second),
	)
	if err != nil {
		t.Skip("broker unavailable: " + err.Error())
	}
	defer client.Close()
	// quick TCP check to ensure broker is reachable; otherwise skip test
	d := net.Dialer{Timeout: 2 * time.Second}
	if conn, err := d.DialContext(context.Background(), "tcp", "localhost:9092"); err != nil {
		t.Skip("broker unavailable: " + err.Error())
	} else {
		conn.Close()
	}
	// attempt metadata request

	// unique identifiers
	uniq := fmt.Sprintf("dq-it-%d", time.Now().UnixNano())
	topic := "dq.it.orders"
	dlqTopic := "dq.it.orders.dlq"
	group := uniq

	// create in‑process producer
	rates := producer.Rates{Missing: 0.15, Dup: 0.15, TypeDrift: 0.15, OOO: 0.15, Lag: 0.15, InvalidJSON: 0.1}
	gen, err := producer.NewGenerator(42, rates, time.Now)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	emitter := producer.NewEmitter([]string{"localhost:9092"}, topic)
	// EnsureTopic is a stub; ignore error.
	_ = emitter.EnsureTopic(context.Background())
	ledgerPath := t.TempDir() + "/ledger.jsonl"
	ledger, err := producer.NewLedger(ledgerPath)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	// produce 300 events quickly
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

	// run consumer
	findingsPath := t.TempDir() + "/findings.jsonl"
	cfg := consumer.Config{
		Bootstrap:    "localhost:9092",
		Topic:        topic,
		Group:        group,
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

	// verify findings file has content
	data, err := os.ReadFile(findingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("findings file empty")
	}
	// simple parse to count unique checks
	var hasMissing, hasDup, hasTypedrift, hasOOO, hasLag, hasInvalid bool
	lines := 0
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		lines++
		if contains(line, "field_missing") {
			hasMissing = true
		}
		if contains(line, "duplicate") {
			hasDup = true
		}
		if contains(line, "type_drift") {
			hasTypedrift = true
		}
		if contains(line, "out_of_order") {
			hasOOO = true
		}
		if contains(line, "lag") {
			hasLag = true
		}
		if contains(line, "invalid_json") {
			hasInvalid = true
		}
	}
	if !(hasMissing && hasDup && hasTypedrift && hasOOO && hasLag && hasInvalid) {
		t.Fatalf("not all check types present in findings (lines=%d)", lines)
	}

	// consume DLQ to ensure messages were produced there
	dlqConsumer, err := kgo.NewClient(
		kgo.SeedBrokers("localhost:9092"),
		kgo.ConsumeTopics(dlqTopic),
		kgo.ConsumerGroup("dlq-consumer-"+uniq),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("dlq client: %v", err)
	}
	defer dlqConsumer.Close()
	var dlqCount int
	dlqCtx, dlqCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dlqCancel()
	for dlqCount < 10 {
		fetches := dlqConsumer.PollFetches(dlqCtx)
		if fetches.Err() != nil {
			t.Fatalf("dlq poll: %v", fetches.Err())
		}
		iter := fetches.RecordIter()
		for !iter.Done() {
			rec := iter.Next()
			if rec != nil {
				dlqCount++
			}
		}
		if dlqCount > 0 {
			break
		}
	}
	if dlqCount == 0 {
		t.Fatalf("no messages in DLQ")
	}

	// compute caught/total using summary logic
	// load findings from JSONL file
	var findings []checks.Finding
	data, err = os.ReadFile(findingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	for _, line := range splitLines(string(data)) {
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
	// ensure caught ratio >= 0.95 for tags with total>0 (except invalid_json rule)
	for tag, total := range summary.TagTotal {
		if total == 0 {
			continue
		}
		caught := summary.Caught[tag]
		if tag == "invalidjson" {
			if caught == 0 {
				t.Fatalf("invalidjson caught should be >0")
			}
			continue
		}
		if float64(caught)/float64(total) < 0.95 {
			t.Fatalf("tag %s caught ratio %.2f < 0.95", tag, float64(caught)/float64(total))
		}
	}
}

// helpers
func splitLines(s string) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
