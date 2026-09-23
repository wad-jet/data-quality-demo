# Background DQ Watcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** фоновый встраиваемый DQ-инспектор `internal/dq.Watcher` на consumer и producer, «коммит только обработанное» (marks-семантика franz-go) и flush-per-line в ledger — at-least-once обработка, never-lose findings.

**Architecture:** tap (ограниченная очередь + блокирующий `Push`) → один фоновый воркер (FIFO, порядок по партиции) делает проверки, findings, DLQ, агрегацию. Consumer получает `kgo.AutoCommitMarks()` + watermark по `Watcher.ProcessedCount()`: офсет коммитится только после обработки воркером. Producer — тот же компонент на исходящий поток (самодиагностика, DLQ нет). `Emitter` и форматы файлов не меняются.

**Tech Stack:** Go 1.26 (module `dqdemo`), stdlib + существующий franz-go v1.22.0 (`kgo.AutoCommitMarks`, `MarkCommitRecords`, `CommitMarkedOffsets`). New deps: нет.

**Spec:** `docs/superpowers/specs/2026-09-23-background-dq-watcher-design.md` (авторитетен при конфликтах; секции §3.1–§3.4, §6, §7).

## Global Constraints
- Go 1.26 (`go.mod: go 1.26.0`); таб-индентация, `gofmt`; таб-индентация и русские doc-комментарии у экспортных идентификаторов (стиль проекта).
- **Нигде в репо не появляется `kgo.WithPools`** — память между поллами удерживает только без пулов (guardrail spec §3.2).
- `internal/dq` — **без импорта kgo** (встраиваемость в любой поток).
- Форматы `findings.jsonl` / `ledger.jsonl` — не меняются. Отчёт consumer'а (`summary.Render()`) — байт-в-байт как сейчас. Флаги CLI consumer'а — не меняются.
- Watcher: никаких дропов (блокирующий `Push`), один воркер, FIFO, собственный ctx (отмена в `Close`).
- Дефолты: `BufferSize` 1024, `CloseTimeout` 30s, producer `-lag-threshold` 60s.
- Integration-тесты: авто-skip, если брокер недоступен на `localhost:9092` (TCP-диал с таймаутом 2 с).
- TDD: каждый шаг-тест сначала RED, затем GREEN, затем commit.
- Commit message: `<type>: <scope> — <что>` (пример: `feat: internal/dq — фоновый DQ-воркер`).
- Проверки после каждого task: `gofmt -l .` пустой, `go vet ./...` чисто, `go build ./...` и `go test ./...` зелёные.

## Review Focus
1. **Воркер медленнее потока (backpressure)** — `Push` блокирует основной цикл, ничего не теряется; после `Close` обработано всё. → T2 `TestBackpressureNoLoss`.
2. **Брокер недоступен в shutdown (DLQ-отправка зависла)** — `Close` ограничен `CloseTimeout`, сервис не виснет. → T2 `TestCloseTimeoutBounded`.
3. **kill -9 в середине прогона** — ledger не теряет последнюю строку (flush); consumer перечитывает хвост, дубли в findings безобидны. → T3 `TestAppendFlushesPerLine`, T4 `TestIntegrationCommitProcessed`.
4. **Повторный `make demo`** — `producer-findings.jsonl` (O_APPEND) не накапливается. → T6 (rm в `clean`/`demo`) + e2e-проверка на гейте 17.
5. **Удержание `*kgo.Record` между поллами** — должно работать только без `kgo.WithPools`. → T4 шаг 6 (grep-верификация).

## File Structure
- Create: `internal/checks/violation.go` + `violation_test.go` — `IsSchemaViolation` (T1)
- Modify: `internal/consumer/consumer.go`, `internal/audit/audit.go` — переезд на общую функцию (T1)
- Create: `internal/dq/watcher.go` + `watcher_test.go` — компонент Watcher (T2, KEY TASK)
- Modify: `internal/producer/ledger.go` + `ledger_test.go` — flush на строку (T3)
- Modify: `internal/consumer/consumer.go` — tap + marks + shutdown (T4, KEY TASK)
- Create: `internal/consumer/commit_integration_test.go` — коммиченный офсет == обработано (T4)
- Modify: `cmd/producer/main.go` + Create `main_test.go` — DQ-тап, флаги, сводка (T5)
- Modify: `Makefile`, `.gitignore` — `producer-findings.jsonl` (T6)

---

### Task 1: `checks.IsSchemaViolation` — единый источник (consumer, dq, audit)

**Tier:** haiku (механика: 1 новая функция + 2 переноса вызовов).

**Files:**
- Create: `internal/checks/violation.go`
- Test: `internal/checks/violation_test.go`
- Modify: `internal/consumer/consumer.go:145` (вызов) и `:181-188` (удалить локальный `isSchemaViolation`)
- Modify: `internal/audit/audit.go:33-34` (удалить `dlqChecks`) и `:286-294` (заменить цикл)

**Interfaces:**
- Produces: `func IsSchemaViolation(check string) bool` в пакете `checks` — используется T2 (воркер) и текущими consumer/audit.

- [ ] **Step 1: Write the failing test**

`internal/checks/violation_test.go`:
```go
package checks

import "testing"

func TestIsSchemaViolation(t *testing.T) {
	cases := []struct {
		check string
		want  bool
	}{
		{"field_missing", true},
		{"type_drift", true},
		{"invalid_json", true},
		{"duplicate", false},
		{"out_of_order", false},
		{"lag", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsSchemaViolation(tc.check); got != tc.want {
			t.Errorf("IsSchemaViolation(%q) = %v, want %v", tc.check, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/checks/ -run TestIsSchemaViolation -v`
Expected: FAIL — «undefined: IsSchemaViolation» (compile error).

- [ ] **Step 3: Implement `IsSchemaViolation`**

`internal/checks/violation.go`:
```go
package checks

// IsSchemaViolation — true для проверок, чьи findings уходят в DLQ
// (нарушения схемы). Единый источник: consumer, internal/dq, audit.
func IsSchemaViolation(check string) bool {
	switch check {
	case "field_missing", "type_drift", "invalid_json":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/checks/ -run TestIsSchemaViolation -v`
Expected: PASS.

- [ ] **Step 5: Мигрировать consumer**

В `internal/consumer/consumer.go`:
- строка 145: `if isSchemaViolation(f.Check) {` → `if checks.IsSchemaViolation(f.Check) {`
- удалить функцию `isSchemaViolation` (строки 181–188).

- [ ] **Step 6: Мигрировать audit**

В `internal/audit/audit.go`:
- удалить `var dlqChecks = []string{...}` (строки 33–34, включая комментарий);
- заменить блок 286–294:
```go
	for _, f := range findings {
		if checks.IsSchemaViolation(f.Check) {
			rep.DLQ.Count++
			rep.DLQ.ByReason[f.Check]++
		}
	}
```
(`checks` уже импортирован в audit.go.)

- [ ] **Step 7: Full verification**

Run: `go build ./... && go test ./internal/checks/... ./internal/audit/... ./internal/consumer/...`
Expected: PASS (существующие тесты consumer/audit не меняют поведение).

- [ ] **Step 8: Commit**

```bash
git add internal/checks/violation.go internal/checks/violation_test.go internal/consumer/consumer.go internal/audit/audit.go
git commit -m "refactor: checks.IsSchemaViolation — единый источник (consumer/dq/audit)"
```

---

### Task 2: `internal/dq.Watcher` — фоновый DQ-инспектор (KEY TASK)

**Tier:** sonnet (конкурентность: очередь, воркер, bounded drain).

**Files:**
- Create: `internal/dq/watcher.go`
- Test: `internal/dq/watcher_test.go`

**Interfaces:**
- Consumes: `checks.Registry`/`checks.Envelope`/`checks.State`/`checks.NewDefault`/`checks.IsSchemaViolation` (T1), `report.NewSummary`/`*report.FindingsWriter` (удовлетворяет `FindingsSink`).
- Produces (использует T4/T5):
  - `type Config struct { Checks checks.Registry; Findings FindingsSink; DLQ DLQSink; BufferSize int; CloseTimeout time.Duration }`
  - `func New(cfg Config) *Watcher`
  - `func (w *Watcher) Start()` — без параметров; собственный ctx (`context.Background`), отмена в `Close`; контракт: `Start` до `Push`, `Close` после `Start`
  - `func (w *Watcher) Push(env checks.Envelope)` — блокирует при полном буфере; только до `Close`
  - `func (w *Watcher) ProcessedCount() map[int32]int64` — обработано по партициям, конкурентно-безопасно
  - `func (w *Watcher) Summary() *report.Summary` — валидно только после `Close()`
  - `func (w *Watcher) Findings() []checks.Finding` — валидно только после `Close()`; для `ComputeCaught`
  - `func (w *Watcher) Close() error` — ограниченный дренаж; при таймауте возвращает `dq.ErrDrainTimeout`
  - `var ErrDrainTimeout = errors.New("dq: drain timeout")`

- [ ] **Step 1: Write the failing tests**

`internal/dq/watcher_test.go`:
```go
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
				if cur := w.ProcessedCount()[p]; cur < prev[p] {
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dq/ -v`
Expected: FAIL — «undefined: New» (compile error).

- [ ] **Step 3: Implement `internal/dq/watcher.go`**

```go
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
	Findings     FindingsSink    // nil = не писать findings (в памяти аккумулируются)
	DLQ          DLQSink         // nil = без DLQ
	BufferSize   int             // ёмкость очереди, дефолт 1024
	CloseTimeout time.Duration   // лимит дренажа в Close, дефолт 30s
}

// FindingsSink — куда воркер пишет findings; удовлетворяет *report.FindingsWriter.
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
	processed   map[int32]int64

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
			return
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
func (w *Watcher) Summary() *report.Summary { return w.summary }

// Findings — валидно только после Close(); для ComputeCaught.
func (w *Watcher) Findings() []checks.Finding { return w.findings }

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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dq/ -v -race`
Expected: PASS (все 8 тестов). `-race` обязателен (конкурентность).

- [ ] **Step 5: Guardrail — no kgo import**

Run: `go list -deps ./internal/dq | grep twmb`
Expected: пусто (зависимости на kgo/franz-go нет).

- [ ] **Step 6: Commit**

```bash
git add internal/dq/
git commit -m "feat: internal/dq — фоновый DQ-воркер (tap, backpressure, bounded drain)"
```

---

### Task 3: Ledger — flush после каждой строки

**Tier:** haiku (1 изменение + 1 тест).

**Files:**
- Modify: `internal/producer/ledger.go:41-54` (`Append`)
- Test: `internal/producer/ledger_test.go` (добавить тест)

**Interfaces:**
- Consumes: ничего нового.
- Produces: поведение — после `Append` строка видна из файла до `Close`.

- [ ] **Step 1: Write the failing test**

Добавить в `internal/producer/ledger_test.go` (импорты `io`, `strings` добавить, если отсутствуют):
```go
// Flush на строку: строка читаема из файла до Close (kill -9 безопасность, spec §3.4).
func TestAppendFlushesPerLine(t *testing.T) {
	path := t.TempDir() + "/ledger.jsonl"
	l, err := NewLedger(path)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	defer l.Close()
	if err := l.Append(LedgerEntry{OrderID: "o1", Ts: time.Now(), Defect: DefectNone}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"order_id":"o1"`) {
		t.Fatalf("line not flushed before Close: %q", string(data))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/producer/ -run TestAppendFlushesPerLine -v`
Expected: FAIL — строка не в файле (буфер 4KB не флешится).

- [ ] **Step 3: Implement flush**

`internal/producer/ledger.go`, `Append` — после `l.writer.Write(append(b, '\n'))`:
```go
	if _, err := l.writer.Write(append(b, '\n')); err != nil {
		return err
	}
	return l.writer.Flush()
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/producer/ -v`
Expected: PASS (оба теста).

- [ ] **Step 5: Commit**

```bash
git add internal/producer/ledger.go internal/producer/ledger_test.go
git commit -m "fix: ledger — flush после каждой строки (kill -9 безопасность)"
```

---

### Task 4: Consumer — tap + «коммит только обработанное» (KEY TASK)

**Tier:** sonnet (marks-семантика franz-go, shutdown-цепочка).

**Files:**
- Modify: `internal/consumer/consumer.go` (перезаписать `Run`, `shutdown`, `New`, struct; удалить inline-проверки)
- Test: `internal/consumer/commit_integration_test.go` (новый)

**Interfaces:**
- Consumes: `dq.New/Start/Push/ProcessedCount/Close/Summary/Findings` (T2), `checks.Envelope`/`checks.IsSchemaViolation` (T1), `kgo.AutoCommitMarks()`, `cl.MarkCommitRecords(rs ...*kgo.Record)`, `cl.CommitMarkedOffsets(ctx)`.
- Produces: публичный API consumer'а не меняется (`New(cfg, findingsPath, ledgerPath)`, `Run(ctx)`); флаги CLI — не меняются.

- [ ] **Step 1: Write the failing integration test**

`internal/consumer/commit_integration_test.go`:
```go
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
	cfg := consumer.Config{
		Bootstrap: "localhost:9092", Topic: topic, Group: uniq, DLQTopic: dlqTopic,
		LagThreshold: 60 * time.Second, StopN: int64(n), IdleStop: 3 * time.Second,
	}
	cons, err := consumer.New(cfg, findingsPath, ledgerPath)
	if err != nil {
		t.Fatalf("consumer new: %v", err)
	}
	ctxRun, cancelRun := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelRun()
	if err := cons.Run(ctxRun); err != nil {
		t.Fatalf("consumer run: %v", err)
	}

	// коммиченный офсет группы == n
	cl, err := kgo.NewClient(kgo.SeedBrokers("localhost:9092"), kgo.DialTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("kgo client: %v", err)
	}
	defer cl.Close()
	req := kmsg.NewPtrOffsetFetchRequest()
	req.Group = uniq
	topicReq := kmsg.NewOffsetFetchRequestTopic()
	topicReq.Topic = topic
	part := kmsg.NewOffsetFetchRequestTopicPartition()
	part.Partition = 0
	topicReq.Partitions = append(topicReq.Partitions, part)
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
				committed = tp.CommittedOffset
			}
		}
	}
	if committed != int64(n) {
		t.Fatalf("committed offset = %d, want %d (commit-processed semantics)", committed, n)
	}
}
```
(Если поля kmsg v1 называются иначе — свериться с `go doc github.com/twmb/franz-go/pkg/kmsg OffsetFetchResponseTopicPartition`; смысл: запрошен офсет группы по (topic, partition 0), ожидаем `n`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/consumer/ -run TestIntegrationCommitProcessed -v`
Expected: FAIL без брокера → SKIP (норма); с брокером (`make broker-up`) → FAIL (текущий код: слепой автокоммит, коммит может опережать обработку, либо марксов нет вовсе — тест падает по semantics).

- [ ] **Step 3: Rewrite `internal/consumer/consumer.go`**

Полный новый контент файла:
```go
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
	cfg          Config
	findingsPath string
	ledgerPath   string
	// runtime fields
	dlqProducer *producer.Emitter
	findingsW   *report.FindingsWriter
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
		cfg:          cfg,
		findingsPath: findingsPath,
		ledgerPath:   ledgerPath,
		dlqProducer:  dlqEm,
		findingsW:    fw,
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
			c.watcher.Push(checks.Envelope{
				Key: rec.Key, Value: rec.Value,
				Partition: rec.Partition, Offset: rec.Offset,
			})
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
	if resp := cl.CommitMarkedOffsets(commitCtx); resp.Err() != nil {
		slog.Error("commit marked offsets", "err", resp.Err())
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
```
Удаляются: `isSchemaViolation` (T1), inline-проверки в цикле, поля `registry`/`state`/`summary`/`findings` (агрегация теперь в Watcher).

- [ ] **Step 4: Build + unit check**

Run: `go build ./... && go vet ./internal/consumer/... && gofmt -l internal/consumer/`
Expected: компилируется, vet чисто, gofmt пуст.

- [ ] **Step 5: Run tests (с брокером, если доступен)**

Run: `go test ./internal/consumer/... -v`
Expected: `TestIntegrationCommitProcessed` PASS (или SKIP без брокера); `TestIntegrationConsumer` (существующий) PASS — findings те же типы, caught/total ≥ 0.95.

- [ ] **Step 6: Guardrail — memory contract**

Run: `grep -rn "WithPools" --include="*.go" .`
Expected: пусто (удержание `*kgo.Record` между поллами безопасно только без пулов).

- [ ] **Step 7: Commit**

```bash
git add internal/consumer/consumer.go internal/consumer/commit_integration_test.go
git commit -m "feat: consumer — фоновые DQ-проверки + commit только обработанное"
```

---

### Task 5: Producer — DQ-тап + флаги + сводка

**Tier:** sonnet (CLI-wiring, новые флаги).

**Files:**
- Modify: `cmd/producer/main.go`
- Test: `cmd/producer/main_test.go` (новый)

**Interfaces:**
- Consumes: `dq.New/Start/Push/Close/Summary` (T2), `checks.NewDefault`/`checks.Envelope`, `report.NewFindingsWriter`.
- Produces: флаги `-findings` (дефолт `producer-findings.jsonl`), `-lag-threshold` (дефолт `60s`); итоговая строка summary получает поле `dq`.

- [ ] **Step 1: Write the failing test**

`cmd/producer/main_test.go`:
```go
package main

import (
	"testing"

	"dqdemo/internal/checks"
	"dqdemo/internal/report"
)

func TestDQField(t *testing.T) {
	s := report.NewSummary()
	s.AddFinding(checks.Finding{Check: "lag"})
	s.AddFinding(checks.Finding{Check: "lag"})
	s.AddFinding(checks.Finding{Check: "duplicate"})
	want := "field_missing=0 type_drift=0 duplicate=1 out_of_order=0 lag=2 invalid_json=0"
	if got := dqField(s); got != want {
		t.Fatalf("dqField = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/producer/ -run TestDQField -v`
Expected: FAIL — «undefined: dqField».

- [ ] **Step 3: Wire the watcher into `cmd/producer/main.go`**

Изменения (imports: `+ "fmt"`, `+ "dqdemo/internal/checks"`, `+ "dqdemo/internal/dq"`, `+ "dqdemo/internal/report"`):
1. Флаги (в блок `var (...)`):
```go
		findingsPath = flag.String("findings", "producer-findings.jsonl", "DQ findings output path")
		lagThreshold = flag.Duration("lag-threshold", 60*time.Second, "Lag check threshold (producer side)")
```
2. После создания `ledger` (до ctx/signal):
```go
	findingsW, err := report.NewFindingsWriter(*findingsPath)
	if err != nil {
		slog.Error("findings", "err", err)
		os.Exit(1)
	}
	watcher := dq.New(dq.Config{
		Checks:   checks.NewDefault(*lagThreshold, time.Now),
		Findings: findingsW,
	})
	watcher.Start()
```
3. В цикле, между `emit.Send(...)` и `ledger.Append(...)` (порядок спеки §3.3):
```go
			// DQ-тап безусловен (не зависит от успеха Send) — семантика как у ledger
			watcher.Push(checks.Envelope{Key: []byte(ev.OrderID), Value: ev.Payload, Partition: 0, Offset: 0})
```
4. Shutdown-блок (заменить `_ = emit.Close(); _ = ledger.Close(); slog.Info(...)`):
```go
	if err := watcher.Close(); err != nil {
		slog.Error("dq drain", "err", err)
	}
	_ = emit.Close()
	_ = ledger.Close()
	slog.Info("summary", "sent", sent, "defects", defects, "dups", dups, "failures", emit.Failures(), "dq", dqField(watcher.Summary()))
```
5. Хелпер (в конец файла, перед `getenvOr`):
```go
// dqField — сводка DQ-файндингов producer'а по типам проверок.
func dqField(s *report.Summary) string {
	var parts []string
	for _, c := range []string{"field_missing", "type_drift", "duplicate", "out_of_order", "lag", "invalid_json"} {
		parts = append(parts, fmt.Sprintf("%s=%d", c, s.ByCheck[c]))
	}
	return strings.Join(parts, " ")
}
```
(import `strings`, если отсутствует).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/producer/ -v`
Expected: PASS.

- [ ] **Step 5: Build + vet**

Run: `go build ./... && go vet ./cmd/producer/...`
Expected: OK.

- [ ] **Step 6: Commit**

```bash
git add cmd/producer/main.go cmd/producer/main_test.go
git commit -m "feat: producer — DQ-тап + -findings/-lag-threshold + dq-сводка"
```

---

### Task 6: Makefile + .gitignore — `producer-findings.jsonl`

**Tier:** haiku.

**Files:**
- Modify: `Makefile` (target `demo` — reset-строка; target `clean`)
- Modify: `.gitignore`

**Interfaces:**
- Produces: идемпотентные повторные `make demo` (файл открывается с O_APPEND).

- [ ] **Step 1: Makefile — demo target**

В target `demo`: строку
```
	echo "[demo] resetting demo artifacts (rm -f ledger.jsonl findings.jsonl)..."; \
	rm -f ledger.jsonl findings.jsonl; \
```
заменить на
```
	echo "[demo] resetting demo artifacts (rm -f ledger.jsonl findings.jsonl producer-findings.jsonl)..."; \
	rm -f ledger.jsonl findings.jsonl producer-findings.jsonl; \
```

- [ ] **Step 2: Makefile — clean target**

Строку
```
	rm -f ledger.jsonl producer-ledger.jsonl findings.jsonl audit-report.json *.log
```
заменить на
```
	rm -f ledger.jsonl producer-ledger.jsonl findings.jsonl producer-findings.jsonl audit-report.json *.log
```

- [ ] **Step 3: .gitignore**

Добавить строку `producer-findings.jsonl` после `findings.jsonl` в блок «runtime artifacts».

- [ ] **Step 4: Verify**

Run:
```bash
touch producer-findings.jsonl && make clean && test ! -f producer-findings.jsonl && echo OK && make build
```
Expected: `OK` + успешная сборка.

- [ ] **Step 5: Full suite**

Run: `go test ./... && gofmt -l .`
Expected: все PASS (integration — SKIP без брокера), gofmt пуст.

- [ ] **Step 6: Commit**

```bash
git add Makefile .gitignore
git commit -m "chore: Makefile/.gitignore — producer-findings.jsonl (clean/demo/gitignore)"
```

---

## Spec Coverage Matrix

| Секция спеки | Требование | Task |
|---|---|---|
| §3.1 | Watcher: очередь/воркер/backpressure/дренаж/DLQ/потокобезопасность/`Findings()` | T2 |
| §3.1 | `IsSchemaViolation` → `internal/checks`; consumer + audit на общей функции | T1 |
| §3.2 | consumer: tap, `AutoCommitMarks`, watermark, shutdown (дренаж → DLQ → commit → отчёт) | T4 |
| §3.2 | findings эквивалентны (LagCheck — на момент обработки воркером) | T2 (фикс. `now` в тестах), T4 (существующий integration) |
| §3.3 | producer: безусловный тап, флаги, `dq`-сводка, порядок shutdown | T5 |
| §3.4 | ledger flush на строку | T3 |
| §4 | crash-гарантии: flush ledger; committed == processed (хвост перечитывается) | T3, T4 |
| §6 | 5 unit-тестов dq + 2 follow-up; unit producer; integration (уникальный топик) | T2 (7/8 — follow-up), T3, T4 |
| §7 | `-findings`, `-lag-threshold` (60s) | T5 |
| §7 | Makefile `clean`/`demo` + `.gitignore` | T6 |
| §7 | README + manual_docs | НЕ SDD: шаг 14 pipeline (скилл manual-docs) |
| §8 | `docs/project-context.md` | НЕ SDD: шаг 12a pipeline |
| §9 | regression-сценарии | entry (шаг 12): `go test ./internal/...` (workdir `.`); `go test ./internal/producer/...`; `[Manual] make demo` |

## Spec Follow-ups (round-2 Minors — учтены)
1. «Пометить остаток до финального watermark (ProcessedCount)» — реализовано в T4 shutdown (пометка только до `ProcessedCount`, хвост после таймаута НЕ помечается → перечитка).
2. `CloseTimeout` в `Config` (тестируемость таймаута) — T2: `Config.CloseTimeout` + `TestCloseTimeoutBounded` + экспорт `ErrDrainTimeout`.
3. Контракт `Findings()` при `FindingsSink = nil` — findings аккумулируются в памяти; T2 `TestNilFindingsSinkStillAccumulates`.
4. In-flight envelope при hard-cancel — воркер делает DLQ-отправку по `workerCtx`: отмена → ошибка → `AddDLQError` → envelope обработан, воркер завершается между envelope'ами; покрыто T2 `TestCloseTimeoutBounded`.
5. Wording-миноры спеки (§5 «findings те же», критерий демо «дефектных findings») — уровень утверждённой спеки, задач не требуют; критерий демо прогоняется на гейте 17 (`make demo`).

## Project Context Changes (применяются на шаге 12a pipeline)
- `docs/project-context.md` §4: добавить модуль `internal/dq` (tap + фоновый воркер, без kgo).
- `docs/project-context.md` §5: consumer — проверки в фоновом воркере, «коммит только обработанное»; producer — DQ-тап + `producer-findings.jsonl`.
- Исправить устаревшие ссылки на audit («фаза 2» — сервис `cmd/audit`/`internal/audit` уже существует).
