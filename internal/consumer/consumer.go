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

type Consumer struct {
	cfg          Config
	findingsPath string
	ledgerPath   string
	// runtime fields
	dlqProducer *producer.Emitter
	findingsW   *report.FindingsWriter
	summary     *report.Summary
	registry    checks.Registry
	state       *checks.State
	// bookkeeping for stop conditions
	processed   int64
	lastMsgTime time.Time
	findings    []checks.Finding
}

func New(cfg Config, findingsPath, ledgerPath string) (*Consumer, error) {
	// create findings writer
	fw, err := report.NewFindingsWriter(findingsPath)
	if err != nil {
		return nil, err
	}
	// create DLQ producer (uses same bootstrap)
	dlqEm := producer.NewEmitter([]string{cfg.Bootstrap}, cfg.DLQTopic)
	if err := dlqEm.EnsureTopic(context.Background()); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("ensure dlq topic: %w", err)
	}

	// registry with default checks
	reg := checks.NewDefault(cfg.LagThreshold, time.Now)

	return &Consumer{
		cfg:          cfg,
		findingsPath: findingsPath,
		ledgerPath:   ledgerPath,
		dlqProducer:  dlqEm,
		findingsW:    fw,
		summary:      report.NewSummary(),
		registry:     reg,
		state:        checks.NewState(),
		lastMsgTime:  time.Now(),
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
	// health‑check
	if err := c.healthCheck(); err != nil {
		return errors.New("kafka health check failed: " + err.Error())
	}

	// create consumer client
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(c.cfg.Bootstrap),
		kgo.ConsumeTopics(c.cfg.Topic),
		kgo.ConsumerGroup(c.cfg.Group),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)

	if err != nil {
		return err
	}
	defer cl.Close()

	for {
		// check stop conditions before polling
		if c.cfg.IdleStop > 0 && time.Since(c.lastMsgTime) >= c.cfg.IdleStop {
			return c.shutdown()
		}
		if c.cfg.StopN > 0 && c.processed >= c.cfg.StopN {
			return c.shutdown()
		}
		select {
		case <-ctx.Done():
			return c.shutdown()
		default:
		}
		pollCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		fetches := cl.PollFetches(pollCtx)
		cancel()
		if ctx.Err() != nil {
			return c.shutdown()
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
			// update bookkeeping
			c.processed++
			c.lastMsgTime = time.Now()
			// build envelope
			env := checks.Envelope{Key: rec.Key, Value: rec.Value, Partition: rec.Partition, Offset: rec.Offset}
			findings := c.registry.Inspect(ctx, env, c.state)
			for _, f := range findings {
				c.findings = append(c.findings, f)
				if err := c.findingsW.Append(f); err != nil {
					slog.Error("append finding", "err", err)
				}
				c.summary.AddFinding(f)
				if checks.IsSchemaViolation(f.Check) {
					hdr := []kgo.RecordHeader{{Key: "dq.reason", Value: []byte(f.Check)}}
					if err := c.dlqProducer.SendWithHeaders(ctx, rec.Key, rec.Value, hdr); err != nil {
						c.summary.AddDLQError()
						slog.Error("DLQ send", "err", err)
					} else {
						c.summary.AddDLQ()
					}
				}
			}
		}
	}
}

func (c *Consumer) shutdown() error {
	// close DLQ producer (stub – just call Close)
	if c.dlqProducer != nil {
		_ = c.dlqProducer.Close()
	}
	// close findings writer
	if c.findingsW != nil {
		_ = c.findingsW.Close()
	}
	// load ledger if requested
	if c.ledgerPath != "" {
		if _, err := os.Stat(c.ledgerPath); err == nil {
			if err = c.summary.LoadLedger(c.ledgerPath); err != nil {
				slog.Error("load ledger", "err", err)
			}
		}
	}
	c.summary.ComputeCaught(c.findings)
	slog.Info("summary", "report", c.summary.Render())
	return nil
}
