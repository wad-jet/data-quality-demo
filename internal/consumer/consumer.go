package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"dqdemo/internal/checks"
	"dqdemo/internal/dq"
	"dqdemo/internal/producer"
	"dqdemo/internal/report"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Config struct {
	Bootstrap, Topic, Group, DLQTopic string
	LagThreshold                      time.Duration
	StopN                             int64         // 0 = off
	IdleStop                          time.Duration // 0 = off
}

// dlqSink — адаптер Emitter к dq.DLQSink (header dq.reason, контракт DLQ).
type dlqSink struct{ em *producer.Emitter }

func (s *dlqSink) SendToDLQ(ctx context.Context, env checks.Envelope, reason string) error {
	hdr := []kgo.RecordHeader{{Key: "dq.reason", Value: []byte(reason)}}
	return s.em.SendWithHeaders(ctx, env.Key, env.Value, hdr)
}

type Consumer struct {
	cfg        Config
	ledgerPath string
	// runtime fields
	dlqProducer *producer.Emitter
	watcher     *dq.Watcher
	// bookkeeping for stop conditions
	processed   int64
	lastMsgTime time.Time
}

func New(cfg Config, findingsPath, ledgerPath string) (*Consumer, error) {
	fw, err := report.NewFindingsWriter(findingsPath)
	if err != nil {
		return nil, err
	}
	dlqEm := producer.NewEmitter([]string{cfg.Bootstrap}, cfg.DLQTopic)
	if err := dlqEm.EnsureTopic(context.Background()); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("ensure dlq topic: %w", err)
	}
	reg := checks.NewDefault(cfg.LagThreshold, time.Now)
	return &Consumer{
		cfg:         cfg,
		ledgerPath:  ledgerPath,
		dlqProducer: dlqEm,
		watcher: dq.New(dq.Config{
			Checks:   reg,
			Findings: fw,
			DLQ:      &dlqSink{em: dlqEm},
		}),
		lastMsgTime: time.Now(),
	}, nil
}

// healthCheck attempts a TCP connection to one of the bootstrap brokers.
// If it cannot connect within 5 seconds it returns an error.
func (c *Consumer) healthCheck() error {
	if c.cfg.Bootstrap == "" {
		return errors.New("bootstrap not provided")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", c.cfg.Bootstrap)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func (c *Consumer) Run(ctx context.Context) error {
	if err := c.healthCheck(); err != nil {
		return errors.New("kafka health check failed: " + err.Error())
	}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(c.cfg.Bootstrap),
		kgo.ConsumeTopics(c.cfg.Topic),
		kgo.ConsumerGroup(c.cfg.Group),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AutoCommitMarks(), // слепой автокоммит отключён: продвигается только отмеченное
	)
	if err != nil {
		return err
	}
	c.watcher.Start()
	var (
		pending   = make(map[int32][]*kgo.Record) // Pushнутые, ещё не помеченные (в порядке офсетов)
		markedCnt = make(map[int32]int)           // сколько в pending[p] уже помечено
	)
	for {
		if c.cfg.IdleStop > 0 && time.Since(c.lastMsgTime) >= c.cfg.IdleStop {
			return c.shutdown(cl, pending, markedCnt)
		}
		if c.cfg.StopN > 0 && c.processed >= c.cfg.StopN {
			return c.shutdown(cl, pending, markedCnt)
		}
		select {
		case <-ctx.Done():
			return c.shutdown(cl, pending, markedCnt)
		default:
		}
		pollCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		fetches := cl.PollFetches(pollCtx)
		cancel()
		if ctx.Err() != nil {
			return c.shutdown(cl, pending, markedCnt)
		}
		if fetches.Err() != nil && !errors.Is(fetches.Err(), context.DeadlineExceeded) {
			slog.Error("fetch error", "err", fetches.Err())
			continue
		}
		iter := fetches.RecordIter()
		for !iter.Done() {
			rec := iter.Next()
			if rec == nil {
				continue
			}
			c.processed++
			c.lastMsgTime = time.Now()
			pending[rec.Partition] = append(pending[rec.Partition], rec)
			// tap: блокирует при полном буфере (backpressure, без дропов)
			c.watcher.Push(checks.Envelope{Key: rec.Key, Value: rec.Value, Partition: rec.Partition, Offset: rec.Offset})
		}
		// watermark: пометить для коммита всё, что воркер обработал
		for p, recs := range pending {
			processed := c.watcher.ProcessedCount()[p]
			m := markedCnt[p]
			for int64(m) < processed && m < len(recs) {
				cl.MarkCommitRecords(recs[m])
				recs[m] = nil // освободить запись
				m++
			}
			markedCnt[p] = m
			if m == len(recs) {
				delete(pending, p)
				delete(markedCnt, p)
			}
		}
	}
}

// shutdown: дренаж воркера → DLQ-эмиттер → пометить остаток до финального
// watermark → коммит (свой ctx: сервисный может быть отменён SIGINT'ом) → отчёт.
func (c *Consumer) shutdown(cl *kgo.Client, pending map[int32][]*kgo.Record, markedCnt map[int32]int) error {
	if err := c.watcher.Close(); err != nil {
		slog.Error("dq drain", "err", err)
	}
	_ = c.dlqProducer.Close()
	for p, recs := range pending {
		processed := c.watcher.ProcessedCount()[p]
		m := markedCnt[p]
		for int64(m) < processed && m < len(recs) {
			cl.MarkCommitRecords(recs[m])
			m++
		}
		markedCnt[p] = m
	}
	commitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := cl.CommitMarkedOffsets(commitCtx); err != nil {
		slog.Error("commit marked offsets", "err", err)
	}
	cancel()
	cl.Close()

	summary := c.watcher.Summary()
	if c.ledgerPath != "" {
		if _, err := os.Stat(c.ledgerPath); err == nil {
			if err := summary.LoadLedger(c.ledgerPath); err != nil {
				slog.Error("load ledger", "err", err)
			}
		}
	}
	summary.ComputeCaught(c.watcher.Findings())
	slog.Info("summary", "report", summary.Render())
	return nil
}
