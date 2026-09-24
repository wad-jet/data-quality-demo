# Final-review fix wave Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply three specified fixes to the `feature/background-dq-watcher` branch: labeled break in producer, pending map compaction in consumer, and removal of dead fields.

**Architecture:** The fixes touch the producer's main loop (event generation) and the consumer's watermark handling and struct definition. They are isolated changes that do not affect other components.

**Tech Stack:** Go language, standard library, project-specific packages.

**Spec:** Align with the final whole-branch review findings.

## Global Constraints
- Code must pass `gofmt -l .` with no output.
- All tests must pass (`go test ./...`).
- Build must succeed (`go build ./...`).
- No new dependencies.

## Review Focus
- Ensure the labeled break correctly exits the outer loop on SIGINT.
- Verify that pending map clean‑up does not cause panics when deleting while ranging.
- Confirm removal of dead struct fields does not break any compile‑time references.

---

### Task 1: Add labeled break in producer loop

**Files:**
- Modify: `cmd/producer/main.go:92-105`

**Interfaces:** None

- [ ] **Step 1: Edit loop to include label `loop:` and change `break` to `break loop` in the `case <-ctx.Done()` branch.**
- [ ] **Step 2: Run `go vet ./...` and `go test ./...` to ensure no regressions.**
- [ ] **Step 3: Commit changes.**

### Task 2: Compact consumer pending map after fully caught up partition

**Files:**
- Modify: `internal/consumer/consumer.go` (watermark loop after record‑iteration)

- [ ] **Step 1: Insert deletion of `pending[p]` and `markedCnt[p]` when `m == len(recs)`.**
- [ ] **Step 2: Run tests and vet.**
- [ ] **Step 3: Commit changes.**

### Task 3: Remove dead fields from consumer struct

**Files:**
- Modify: `internal/consumer/consumer.go` struct definition (remove `findingsPath` and `findingsW`).
- Modify: `internal/consumer/consumer.go` New function to drop initializers for those fields.
- Potentially adjust imports (remove `report` if unused).

- [ ] **Step 1: Delete the two fields and their assignments in `New`.**
- [ ] **Step 2: Run `go vet` to ensure no unused imports remain; clean them up.**
- [ ] **Step 3: Run full test suite.**
- [ ] **Step 4: Commit changes.**

### Task 4: Verification and final commit

- [ ] **Run formatting, build, vet, tests.**
- [ ] **Perform manual SIGINT check on built producer binary.**
- [ ] **Create final commit with message `fix: final review — producer SIGINT labeled break; consumer pending compaction + dead fields`.**

---
