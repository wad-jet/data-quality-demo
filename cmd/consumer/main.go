package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dqdemo/internal/consumer"
)

func main() {
	var (
		bootstrap  = flag.String("bootstrap", getenvOr("DQ_BOOTSTRAP", "localhost:9092"), "Kafka bootstrap")
		topic      = flag.String("topic", "dq.orders", "Topic to consume")
		group      = flag.String("group", "dq-demo", "Consumer group")
		dlqTopic   = flag.String("dlq-topic", "dq.orders.dlq", "DLQ topic")
		findings   = flag.String("findings", "findings.jsonl", "Findings output path")
		stopN      = flag.Int64("stop", 0, "Stop after N messages (0 = off)")
		idleStop   = flag.Duration("idle-stop", 0, "Stop after idle duration (0 = off)")
		lagThresh  = flag.Duration("lag-threshold", 60*time.Second, "Lag threshold")
		ledgerPath = flag.String("ledger", "", "Path to producer ledger (optional)")
	)
	flag.Parse()

	cfg := consumer.Config{
		Bootstrap:    *bootstrap,
		Topic:        *topic,
		Group:        *group,
		DLQTopic:     *dlqTopic,
		LagThreshold: *lagThresh,
		StopN:        *stopN,
		IdleStop:     *idleStop,
	}
	cons, err := consumer.New(cfg, *findings, *ledgerPath)
	if err != nil {
		slog.Error("consumer init", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigc; cancel() }()
	if err = cons.Run(ctx); err != nil {
		if ctx.Err() == nil { // error not due to cancellation
			slog.Error("consumer run", "err", err)
			os.Exit(1)
		}
	}
}

func getenvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
