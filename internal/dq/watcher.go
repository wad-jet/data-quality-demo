// Package dq — фоновый инспектор качества: tap (ограниченная очередь)
// и один воркер, который исполняет проверки, findings и DLQ (spec §3.1).
package dq

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"dqdemo/internal/checks"
	"dqdemo/internal/report"
)

// ErrDrainTimeout — Close() не дождался опустошения очереди за CloseTimeout.
var ErrDrainTimeout = errors.New("dq: drain timeout")

type Config struct {
	Checks       checks.Registry
	Findings     FindingsSink  // nil = не писать findings (в памяти аккумулируются)
	DLQ          DLQSink       // nil = без DLQ
	BufferSize   int           // ёмкость очереди, дефолт 1024
	CloseTimeout time.Duration // лимит дренажа в Close, дефолт 30s
}

// FindingsSink — куда воркер пишет findings; удовлетворяет *report.FindingsWriter (или любой тип с методом Append и Close).
type FindingsSink interface {
	Append(checks.Finding) error
	Close() error
}

// DLQSink — отправка нарушающих схему сообщений в DLQ.
type DLQSink interface {
	SendToDLQ(ctx context.Context, env checks.Envelope, reason string) error
}

type Watcher struct {
	cfg      Config
	q        chan checks.Envelope
	state    *checks.State
	summary  *report.Summary
	findings []checks.Finding

	processedMu sync.Mutex
	// watermark: сколько envelope ПРОРАБОТАНО воркером по партициям (не поставлено в очередь)
	processed map[int32]int64

	wg           sync.WaitGroup // Push: Add до отправки в канал; воркер: Done после обработки
	workerCtx    context.Context
	workerCancel context.CancelFunc
	workerDone   chan struct{}
	closed       atomic.Bool
}

func New(cfg Config) *Watcher {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1024
	}
	if cfg.CloseTimeout <= 0 {
		cfg.CloseTimeout = 30 * time.Second
	}
	return &Watcher{
		cfg:       cfg,
		q:         make(chan checks.Envelope, cfg.BufferSize),
		state:     checks.NewState(),
		summary:   report.NewSummary(),
		processed: make(map[int32]int64),
	}
}

// Start запускает фонового воркера (собственный ctx, отмена в Close).
func (w *Watcher) Start() {
	w.workerCtx, w.workerCancel = context.WithCancel(context.Background())
	w.workerDone = make(chan struct{})
	go w.run()
}

func (w *Watcher) run() {
	defer close(w.workerDone)
	for {
		select {
		case env := <-w.q:
			w.process(env)
			w.wg.Done()
		case <-w.workerCtx.Done():
			// ctx отменён (таймаут дренажа): освободить WaitGroup для остатка очереди
			for {
				select {
				case <-w.q:
					w.wg.Done()
				default:
					return
				}
			}
		}
	}
}

// Push передаёт envelope воркеру; блокирует при полном буфере (backpressure,
// без дропов). Вызывать только до Close.
func (w *Watcher) Push(env checks.Envelope) {
	if w.closed.Load() {
		panic("dq: Push after Close")
	}
	w.wg.Add(1)
	w.q <- env
}

func (w *Watcher) process(env checks.Envelope) {
	for _, f := range w.cfg.Checks.Inspect(w.workerCtx, env, w.state) {
		w.findings = append(w.findings, f)
		w.summary.AddFinding(f)
		if w.cfg.Findings != nil {
			if err := w.cfg.Findings.Append(f); err != nil {
				slog.Error("dq: findings append", "err", err)
			}
		}
		if w.cfg.DLQ != nil && checks.IsSchemaViolation(f.Check) {
			if err := w.cfg.DLQ.SendToDLQ(w.workerCtx, env, f.Check); err != nil {
				w.summary.AddDLQError()
				slog.Error("dq: dlq send", "err", err)
			} else {
				w.summary.AddDLQ()
			}
			// ошибка DLQ: envelope всё равно обработан (watermark продвигается)
		}
	}
	// watermark продвигается после обработки envelope
	w.processedMu.Lock()
	w.processed[env.Partition]++
	w.processedMu.Unlock()
}

// ProcessedCount — сколько envelope обработано по партициям (конкурентно-безопасно).
func (w *Watcher) ProcessedCount() map[int32]int64 {
	w.processedMu.Lock()
	defer w.processedMu.Unlock()
	out := make(map[int32]int64, len(w.processed))
	for p, n := range w.processed {
		out[p] = n
	}
	return out
}

// Summary — валидно только после Close().
func (w *Watcher) Summary() *report.Summary {
	if !w.closed.Load() {
		panic("dq: Summary before Close")
	}
	return w.summary
}

// Findings — валидно только после Close(); для ComputeCaught.
func (w *Watcher) Findings() []checks.Finding {
	if !w.closed.Load() {
		panic("dq: Findings before Close")
	}
	return w.findings
}

// Close — ограниченный дренаж: дождись обработки всех Pushнутых envelope или
// CloseTimeout (далее — принудительная отмена воркера; необработанный хвост
// логируется и считается потерянным). Возвращает ErrDrainTimeout при таймауте.
func (w *Watcher) Close() error {
	w.closed.Store(true)
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	var err error
	select {
	case <-done:
	case <-time.After(w.cfg.CloseTimeout):
		err = ErrDrainTimeout
	}
	w.workerCancel()
	<-w.workerDone
	if w.cfg.Findings != nil {
		if cerr := w.cfg.Findings.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	if err != nil {
		slog.Warn("dq: drain timeout, tail of queue unprocessed", "timeout", w.cfg.CloseTimeout)
	}
	return err
}
