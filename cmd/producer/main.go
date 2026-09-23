package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dqdemo/internal/producer"
)

func main() {
	var (
		bootstrap    = flag.String("bootstrap", getenvOr("DQ_BOOTSTRAP", "localhost:9092"), "Kafka bootstrap servers")
		topic        = flag.String("topic", "dq.orders", "Kafka topic")
		count        = flag.Int64("count", 0, "Number of messages to send (0 = infinite)")
		rate         = flag.Int("rate", 100, "Messages per second")
		errMissing   = flag.Float64("err-missing", 0.05, "Error rate missing")
		errDup       = flag.Float64("err-dup", 0.05, "Error rate duplicate")
		errTypedrift = flag.Float64("err-typedrift", 0.05, "Error rate type drift")
		errOOO       = flag.Float64("err-ooo", 0.05, "Error rate out of order")
		errLag       = flag.Float64("err-lag", 0.05, "Error rate lag")
		errInvalid   = flag.Float64("err-invalidjson", 0.02, "Error rate invalid JSON")
		seed         = flag.Int64("seed", 0, "Random seed (0 = time.Now)")
		ledgerPath   = flag.String("ledger", "producer-ledger.jsonl", "Ledger output path")
	)
	flag.Parse()

	// Setup generator rates
	rates := producer.Rates{
		Missing:     *errMissing,
		Dup:         *errDup,
		TypeDrift:   *errTypedrift,
		OOO:         *errOOO,
		Lag:         *errLag,
		InvalidJSON: *errInvalid,
	}
	genSeed := *seed
	if genSeed == 0 {
		genSeed = time.Now().UnixNano()
	}
	gen, err := producer.NewGenerator(genSeed, rates, time.Now)
	if err != nil {
		slog.Error("generator", "err", err)
		os.Exit(1)
	}
	emit := producer.NewEmitter([]string{*bootstrap}, *topic)
	// EnsureTopic is a no‑op stub; ignore error.
	_ = emit.EnsureTopic(context.Background())
	ledger, err := producer.NewLedger(*ledgerPath)
	if err != nil {
		slog.Error("ledger", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// handle termination signals
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigc
		cancel()
	}()

	ticker := time.NewTicker(time.Second / time.Duration(*rate))
	defer ticker.Stop()

	var sent, defects, dups int64
	for {
		if *count > 0 && sent >= *count {
			break
		}
		select {
		case <-ctx.Done():
			break
		case <-ticker.C:
			ev := gen.Next()
			if err := emit.Send(ctx, []byte(ev.OrderID), ev.Payload); err != nil {
				// ignore error – failures counted via emitter.Failures()
			}
			if err := ledger.Append(producer.LedgerEntry{OrderID: ev.OrderID, Ts: ev.Ts, Defect: ev.Defect}); err != nil {
				slog.Error("ledger append", "err", err)
			}
			sent++
			if ev.Defect != producer.DefectNone {
				defects++
			}
			if ev.Defect == producer.DefectDup {
				dups++
			}
		}
	}
	// close resources
	_ = emit.Close()
	_ = ledger.Close()
	slog.Info("summary", "sent", sent, "defects", defects, "dups", dups, "failures", emit.Failures())
}

func getenvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
