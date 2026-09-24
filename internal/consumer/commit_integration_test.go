package consumer_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"dqdemo/internal/consumer"
	"dqdemo/internal/producer"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// TestIntegrationCommitProcessed — коммиченный офсет consumer-группы ==
// число обработанных сообщений (spec §6). Уникальный топик: коммиченный
// офсет абсолютный, на фиксированном топике данные накапливаются между прогонами.
func TestIntegrationCommitProcessed(t *testing.T) {
	d := net.Dialer{Timeout: 2 * time.Second}
	if conn, err := d.DialContext(context.Background(), "tcp", "localhost:9092"); err != nil {
		t.Skip("broker unavailable: " + err.Error())
	} else {
		conn.Close()
	}

	uniq := fmt.Sprintf("dq-commit-%d", time.Now().UnixNano())
	topic := "dq.it.commit." + uniq
	dlqTopic := topic + ".dlq"

	rates := producer.Rates{Missing: 0.15, Dup: 0.15, TypeDrift: 0.15, OOO: 0.15, Lag: 0.15, InvalidJSON: 0.1}
	gen, err := producer.NewGenerator(7, rates, time.Now)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	const n = 200
	emit := producer.NewEmitter([]string{"localhost:9092"}, topic)
	_ = emit.EnsureTopic(context.Background())
	ledgerPath := t.TempDir() + "/ledger.jsonl"
	ledger, err := producer.NewLedger(ledgerPath)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	for i := 0; i < n; i++ {
		ev := gen.Next()
		if err := emit.Send(context.Background(), []byte(ev.OrderID), ev.Payload); err != nil {
			t.Fatalf("send: %v", err)
		}
		_ = ledger.Append(producer.LedgerEntry{OrderID: ev.OrderID, Ts: ev.Ts, Defect: ev.Defect})
	}
	emit.Close()
	ledger.Close()

	findingsPath := t.TempDir() + "/findings.jsonl"
	cfg := consumer.Config{Bootstrap: "localhost:9092", Topic: topic, Group: uniq, DLQTopic: dlqTopic,
		LagThreshold: 60 * time.Second, StopN: int64(n), IdleStop: 3 * time.Second}
	cons, err := consumer.New(cfg, findingsPath, ledgerPath)
	if err != nil {
		t.Fatalf("consumer new: %v", err)
	}
	ctxRun, cancelRun := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelRun()
	if err := cons.Run(ctxRun); err != nil {
		t.Fatalf("consumer run: %v", err)
	}

	cl, err := kgo.NewClient(kgo.SeedBrokers("localhost:9092"), kgo.DialTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("kgo client: %v", err)
	}
	defer cl.Close()
	req := kmsg.NewPtrOffsetFetchRequest()
	req.Group = uniq
	topicReq := kmsg.NewOffsetFetchRequestTopic()
	topicReq.Topic = topic
	topicReq.Partitions = []int32{0}
	req.Topics = append(req.Topics, topicReq)
	resp, err := req.RequestWith(context.Background(), cl)
	if err != nil {
		t.Fatalf("offset fetch: %v", err)
	}
	var committed int64 = -1
	for _, t := range resp.Topics {
		if t.Topic != topic {
			continue
		}
		for _, tp := range t.Partitions {
			if tp.Partition == 0 && tp.ErrorCode == 0 {
				committed = tp.Offset
			}
		}
	}
	if committed != int64(n) {
		t.Fatalf("committed offset = %d, want %d (commit-processed semantics)", committed, n)
	}
}
