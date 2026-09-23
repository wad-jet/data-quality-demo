---
version: 1
feature: background-dq-watcher
added: 2026-09-23
status: active
risk: medium
---

# Regression: background-dq-watcher

Фоновый DQ-инспектор `internal/dq.Watcher` (consumer + producer),
«коммит только обработанное» (marks-семантика franz-go), ledger flush-per-line.
Spec: `docs/superpowers/specs/2026-09-23-background-dq-watcher-design.md`.

## Сценарии

- path: internal/consumer/consumer.go
  run: "go test ./internal/..."
  workdir: "."
- path: internal/producer/ledger.go
  run: "go test ./internal/producer/..."
  workdir: "."
- path: internal/checks/violation.go
  run: "go test ./internal/..."
  workdir: "."
- path: internal/dq/watcher.go
  run: "go test ./internal/dq/..."
  workdir: "."
- path: cmd/producer/main.go
  run: "go test ./cmd/producer/..."
  workdir: "."
- e2e
  run: "[Manual] make demo"
  note: нужен docker (RedPanda); при недоступности — фиксация на гейте 17
