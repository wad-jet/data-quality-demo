package dq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dqdemo/internal/checks"
)

// validPayload — корректный event (все 4 поля).
func validPayload(id, ts string) []byte {
	return []byte(fmt.Sprintf(`{"order_id":%q,"amount":10,"currency":"USD","ts":%q}`, id, ts))
}

// fixedNow — фиксированное «сейчас» для детерминизма lag-проверки.
var fixedNow = time.Date(2026, 9, 23, 10, 0, 30, 0, time.UTC)

func defaultReg() checks.Registry {
	return checks.NewDefault(60*time.Second, func() time.Time { return fixedNow })
}

type fakeSink struct {
	mu     sync.Mutex
	f      []checks.Finding
	closed bool
}

func (f *fakeSink) Append(fnd checks.Finding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.f = append(f.f, fnd)
	return nil
}

func (f *fakeSink) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.f)
}

type dlqCall struct {
	env    checks.Envelope
	reason string
}

type fakeDLQ struct {
	mu    sync.Mutex
	calls []dlqCall
	err   error
}

func (f *fakeDLQ) SendToDLQ(ctx context.Context, env checks.Envelope, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, dlqCall{env: env, reason: reason})
	return nil
}

func (f *fakeDLQ) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// slowCheck — искусственно медленная проверка (backpressure/таймаут).
type slowCheck struct {
	delay time.Duration
	count int64
}

func (s *slowCheck) Name() string { return "slow" }

func (s *slowCheck) Inspect(ctx context.Context, env checks.Envelope, st *checks.State) []checks.Finding {
	atomic.AddInt64(&s.count, 1)
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
	}
	return nil
}

func (s *slowCheck) total() int64 { return atomic.LoadInt64(&s.count) }

// 1. Drain: всё Pushнутое обработано, findings записаны.
func TestDrainAll(t *testing.T) {
	sink := &fakeSink{}
	w := New(Config{Checks: defaultReg(), Findings: sink})
	w.Start()
	for i := 0; i < 50; i++ {
		w.Push(checks.Envelope{Key: []byte(fmt.Sprintf("o%d", i)), Value: validPayload(fmt.Sprintf("o%d", i), "2026-09-23T10:00:00Z"), Partition: 0, Offset: int64(i)})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := w.ProcessedCount()[0]; n != 50 {
		t.Fatalf("processed = %d, want 50", n)
	}
	if n := sink.count(); n != 0 {
		t.Fatalf("clean events produced %d findings, want 0", n)
	}
}

// 2. Порядок: воркер видит сообщения в порядке Push (dup/ooo/firends).
func TestOrderFindings(t *testing.T) {
	sink := &fakeSink{}
	w := New(Config{Checks: defaultReg(), Findings: sink})
	w.Start()
	envs := []checks.Envelope{
		{Key: []byte("o1"), Value: validPayload("o1", "2026-09-23T10:00:00Z"), Partition: 0, Offset: 0},
		{Key: []byte("o2"), Value: validPayload("o2", "2026-09-23T10:00:10Z"), Partition: 0, Offset: 1},
		{Key: []byte("o1"), Value: validPayload("o1", "2026-09-23T10:00:10Z"), Partition: 0, Offset: 2}, // duplicate
		{Key: []byte("o3"), Value: validPayload("o3", "2026-09-23T10:00:05Z"), Partition: 0, Offset: 3}, // out_of_order
		{Key: []byte("bad"), Value: []byte("{broken"), Partition: 0, Offset: 4},                          // invalid_json
		{Key: []byte("o4"), Value: []byte(`{"order_id":"o4","amount":1,"currency":"USD"}`), Partition: 0, Offset: 5}, // field_missing (ts)
	}
	for _, e := range envs {
		w.Push(e)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := map[string]int{}
	for _, f := range sink.f {
		got[f.Check]++
	}
	want := map[string]int{"duplicate": 1, "out_of_order": 1, "invalid_json": 1, "field_missing": 1}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("finding %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// 3. DLQ: только schema-violations, reason = имя проверки.
func TestDLQOnlySchemaViolations(t *testing.T) {
	dlq := &fakeDLQ{}
	w := New(Config{Checks: defaultReg(), DLQ: dlq})
	w.Start()
	w.Push(checks.Envelope{Value: []byte(`{"order_id":"o4","amount":1,"currency":"USD"}`), Partition: 0, Offset: 0}) // field_missing
	w.Push(checks.Envelope{Value: validPayload("o1", "2026-09-23T10:00:00Z"), Partition: 0, Offset: 1})
	w.Push(checks.Envelope{Value: validPayload("o1", "2026-09-23T10:00:00Z"), Partition: 0, Offset: 2}) // duplicate
	w.Push(checks.Envelope{Value: validPayload("o2", "2026-09-23T09:59:00Z"), Partition: 0, Offset: 3}) // lag
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := dlq.count(); n != 1 {
		t.Fatalf("DLQ calls = %d, want 1", n)
	}
	if r := dlq.calls[0].reason; r != "field_missing" {
		t.Fatalf("DLQ reason = %q, want field_missing", r)
	}
}

// 4. Ошибка DLQ: лог + счётчик, envelope считается обработанным.
func TestDLQErrorCountsAsProcessed(t *testing.T) {
	dlq := &fakeDLQ{err: errors.New("broker down")}
	w := New(Config{Checks: defaultReg(), DLQ: dlq})
	w.Start()
	w.Push(checks.Envelope{Value: []byte(`{"order_id":"o4","amount":1,"currency":"USD"}`), Partition: 0, Offset: 0})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := w.ProcessedCount()[0]; n != 1 {
		t.Fatalf("processed = %d, want 1 (DLQ error must not block watermark)", n)
	}
	s := w.Summary()
	if s.DLQ != 0 || s.DLQErrors != 1 {
		t.Fatalf("DLQ=%d DLQErrors=%d, want 0/1", s.DLQ, s.DLQErrors)
	}
}

// 5. Backpressure: малый буфер + медленный воркер — ничего не теряется.
func TestBackpressureNoLoss(t *testing.T) {
	slow := &slowCheck{delay: 2 * time.Millisecond}
	w := New(Config{Checks: checks.Registry{slow}, BufferSize: 4})
	w.Start()
	const n = 100
	for i := 0; i < n; i++ {
		w.Push(checks.Envelope{Value: validPayload("o", "2026-09-23T10:00:00Z"), Partition: 0, Offset: int64(i)})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c := slow.total(); c != n {
		t.Fatalf("processed %d of %d", c, n)
	}
}

// 6. Watermark: ProcessedCount монотонно неубывает по партициям.
func TestProcessedCountMonotonic(t *testing.T) {
	slow := &slowCheck{delay: 1 * time.Millisecond}
	w := New(Config{Checks: checks.Registry{slow}, BufferSize: 16})
	w.Start()
	var prev [2]int64
	const n = 100
	for i := 0; i < n; i++ {
		p := int32(i % 2)
		w.Push(checks.Envelope{Value: validPayload("o", "2026-09-23T10:00:00Z"), Partition: p, Offset: int64(i)})
		if i%10 == 9 {
		for p := int32(0); p < 2; p++ {
			cur := w.ProcessedCount()[p]
			if cur < prev[p] {
				t.Fatalf("partition %d watermark went backwards: %d -> %d", p, prev[p], cur)
			}
			prev[p] = cur
		}

		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if prev[0]+prev[1] != n {
		t.Fatalf("total processed = %d, want %d", prev[0]+prev[1], n)
	}
}

// 7. (follow-up) CloseTimeout: дренаж ограничен, Close не виснет.
func TestCloseTimeoutBounded(t *testing.T) {
	slow := &slowCheck{delay: 50 * time.Millisecond}
	w := New(Config{Checks: checks.Registry{slow}, BufferSize: 16, CloseTimeout: 100 * time.Millisecond})
	w.Start()
	const n = 100 // 100 x 50ms = 5s работы против 100ms дренажа
	for i := 0; i < n; i++ {
		w.Push(checks.Envelope{Value: validPayload("o", "2026-09-23T10:00:00Z"), Partition: 0, Offset: int64(i)})
	}
	start := time.Now()
	err := w.Close()
	elapsed := time.Since(start)
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("Close err = %v, want ErrDrainTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Close hung: %v", elapsed)
	}
	if c := slow.total(); c >= n {
		t.Fatalf("all %d processed despite timeout", n)
	}
}

// 8. (follow-up) FindingsSink = nil: findings всё равно аккумулируются в памяти.
func TestNilFindingsSinkStillAccumulates(t *testing.T) {
	w := New(Config{Checks: defaultReg()})
	w.Start()
	w.Push(checks.Envelope{Value: []byte("{broken"), Partition: 0, Offset: 0})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := len(w.Findings()); n != 1 {
		t.Fatalf("Findings() = %d, want 1", n)
	}
	if w.Summary().Total != 1 {
		t.Fatalf("Summary().Total = %d, want 1", w.Summary().Total)
	}
}
