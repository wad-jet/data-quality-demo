# Plan: Фаза 1 — Упаковка демо (feature/demo-packaging)

Spec: `docs/superpowers/specs/2026-09-23-data-quality-demo-design.md` (архитектура); DoD — `docs/roadmap.md`, Фаза 1.

## Global Constraints
- Только 2 новых файла: `Makefile`, `README.md`. Изменения Go-кода запрещены (флаги уже достаточны).
- Детерминизм: producer `-seed 42 -count 1000 -rate 200`; consumer `-stop 1000`. Тема `dq.orders`, bootstrap `localhost:9092` (дефолты уже в CLI).
- Брокер — существующий `docker-compose.yml` (RedPanda single-node).
- DoD: одна команда `make demo` → broker → producer → consumer → отчёт (caught/total = 100% × 6 тегов) за < 60 с; отчёт печатается consumer'ом (stdout + findings.jsonl).

## Task 1: Makefile
Цели:
- `make demo` — полный пайплайн: `docker compose up -d` → ожидание брокера (retry-цикл `nc -z localhost 9092`, timeout 60 с) → `make build` → producer (флаги детерминизма, `-ledger ledger.jsonl`) → consumer (`-stop 1000 -ledger ledger.jsonl -findings findings.jsonl`) → вывод отчёта (stdout consumer'а + путь к findings.jsonl) → `docker compose down` (через trap, в т.ч. по Ctrl-C).
- `make broker-up`, `make broker-down` — только брокер.
- `make build` — `go build -o bin/producer ./cmd/producer`, `go build -o bin/consumer ./cmd/consumer`.
- `make test` — `go test ./...`.
- Очистка артефактов: `make clean` (bin/, ledger.jsonl, findings.jsonl, *.log).
- Shell: /bin/sh, macOS-совместимо (nc, go, docker в PATH).

Верификация: живой прогон `make demo` при запущенном Docker; в stdout consumer'а — caught/total 100% по 6 тегам; broker после прогона остановлен.

## Task 2: README.md
Секции: Назначение (1 абзац) · Требования (Go 1.26+, Docker) · Quickstart (`make demo`) · Ручной запуск (команды producer/consumer с флагами) · Таблица 6 проверок (тег, что ловит) · Как читать отчёт (caught/total, DLQ topic, findings.jsonl, ledger) · Ссылка на spec.

Верификация: визуальная сверка с фактическими флагами CLI (cmd/producer/main.go, cmd/consumer/main.go).
