---
version: 1
feature: audit-dlq-report
added: 2026-09-24
status: active
risk: medium
---

# Regression: audit-dlq-report

Audit читает live DLQ-топик (kmsg precheck + group-less consume) и рендерит
отчёт в MD/HTML; `make demo` запускает audit до broker-down. Cross-layer:
CLI → internal/audit → брокер.
Spec: `docs/superpowers/specs/2026-09-24-audit-dlq-report-design.md`.

## Сценарии

- path: cmd/audit/main.go
  run: "./bin/audit -ledger ledger.jsonl -findings findings.jsonl"
  workdir: "."
- path: internal/audit/dlq.go
  run: "go test ./internal/audit/... -count=1"
  workdir: "."
- path: Makefile
  run: "make demo"
  workdir: "."
