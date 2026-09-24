# Project Context — data_quality_demo

## 1. Название и назначение

Демо-проект для проверки инструментов контроля качества стриминговых данных:
показывает, что инструменты ловят типовые проблемы данных (null/пропуски,
дубликаты, сдвиги типов, нарушенная последовательность, lag) на живом потоке.
Аудитория: разработчики, оценивающие инструменты качества данных.

Тип проекта: **monorepo** (несколько Go-пакетов в одном репозитории).

## 2. Цели и метрики успеха

- Цель: продемонстрировать на живом потоке (RedPanda), что проверки качества
  ловят типовые проблемы данных — до и после.
- Успех: демо-прогон показывает ожидаемые находки по каждому типу проблемы.
- Non-goals: продакшн-надежность и масштабирование — это демо.

## 3. Стек технологий

- Язык: **Go**.
- Сторонка: **RedPanda** (Kafka-совместимый стриминг-брокер), локально через
  docker-compose.
- Инструменты: `go test` (тест-раннер), `golangci-lint` (линтер), менеджер
  пакетов — Go modules.
- Версионирование: **нет** (demo, без релизов; pipeline-хук версии не
  срабатывает).

## 4. Архитектура

Три независимых Go-сервиса, поток: **producer → RedPanda → consumer →
findings (JSONL) + DLQ + агрегат с caught/total**; фоновый DQ-инспектор
(`internal/dq.Watcher`) прикреплен к обеим сторонам потока; audit — оффлайн.

- `cmd/producer` — генерация order-событий, вживление дефектов по заданным
  процентам (missing/dup/typedrift/ooo/lag/invalidjson), запись ledger (JSONL,
  ground truth; flush на строку), отправка в топик `dq.orders` + фоновый
  DQ-тап в `producer-findings.jsonl`.
- `cmd/consumer` — чтение топика, тап в фоновый DQ-воркер (`internal/dq`):
  checks, DLQ (`dq.orders.dlq`) для schema-violations, findings JSONL, агрегат
  в stdout (caught/total по ledger); «коммит только обработанное»
  (franz-go marks-семантика, at-least-once).
- `cmd/audit` — аудит: precision/recall по тегам, DLQ по причинам, таймлайн
  (ledger + findings). Режимы: build (офлайн из файлов; опционально
  `-dlq-topic` — чтение DLQ-топика с брокера и сверка) и render
  (`-from report.json -format text|md|html`).
- Топики создаются явно (1 партиция, без auto-create). Kafka-клиент — franz-go;
  точки изоляции: `internal/producer/emit.go` (запись), `internal/consumer` (чтение).

IMPORTANT: checks — чистая логика без сети (unit без брокера); интеграции с
RedPanda изолированы в emit.go и consumer; out_of_order — по порядку получения
(1 партиция), `LastTS` монотонный.

Дизайн-спеки: `docs/superpowers/specs/2026-09-23-data-quality-demo-design.md`,
`docs/superpowers/specs/2026-09-23-background-dq-watcher-design.md`
(фоновый DQ-инспектор, crash-гарантии),
`docs/superpowers/specs/2026-09-24-audit-dlq-report-design.md` (audit: DLQ-топик +
рендер MD/HTML; idle-stop чтение DLQ — ListOffsets сломан в RedPanda v26.2.3).

## 5. Домены / модули

| Пакет | Ответственность |
|---|---|
| `cmd/producer`, `cmd/consumer`, `cmd/audit` | CLI-входы сервисов (флаги, slog, SIGINT) |
| `internal/events` | схема события + строгий decode |
| `internal/producer` | генератор (seeded), инжектор дефектов, ledger (flush на строку), kafka-write |
| `internal/checks` | Check/State/Registry + 4 проверки (6 finding-типов), `IsSchemaViolation` |
| `internal/dq` | фоновый DQ-инспектор: tap (ограниченная очередь) + один воркер (checks, findings, DLQ, агрегация); без kgo |
| `internal/consumer` | wiring: read → DQ-тап → marks-коммит («только обработанное») → report |
| `internal/report` | findings JSONL, агрегация, caught/total |
| `internal/audit` | аудит: precision/recall по тегам, DLQ (офлайн + из топика, idle-stop), таймлайн; рендер отчёта text/md/html, LoadJSON |

Каталоги: `cmd/{producer,consumer,audit}/`, `internal/{events,producer,checks,
dq,consumer,report,audit}/`, `docker-compose.yml`.

## 6. Ограничения и допущения

- RedPanda только локально (docker-compose), один узел.
- Малые объёмы: тысячи сообщений.
- Синтетические данные.
- Все артефакты прогонов — в `out/` (gitignore, создаётся автоматически;
  `make clean` удаляет). У брокера нет volumes: данные топиков стираются при
  `docker compose down` — `audit -dlq-topic` работает только пока брокер жив
  (в `make demo` audit прогоняется до broker-down).

## 7. Риски

| Риск | Митигация |
|---|---|
| Доступность RedPanda (docker/сеть) | health-check перед прогоном; integration-тесты в skip-режиме без брокера |
| Нестабильность стриминга (порядок, дубли, lag) | идемпотентные проверки; явная обработка out-of-order и дублей |

## 8. Команда и процессы

- Ветки: `feature/<kebab-case>` по конвенции maestro; коммиты — вручную.
- Code review: агент `code-reviewer` в pipeline maestro.
- CI/CD: нет.

## 9. Критерии приёмки качества

DoD фичи:
1. `go test ./...` зелёный.
2. `go vet ./...` (через `golangci-lint run`) чистый.
3. Демо-прогон показывает ожидаемые находки по типам проблем.

Гейты перед merge: пункт 1–2 обязательны; пункт 3 — для фич, затрагивающих
поток producer→checks→report.

## 10. Тестирование

- **unit**: чистые функции checks и report — без сети.
- **integration**: consumer + RedPanda (docker) — автоматически skip, если
  брокер недоступен.
- Fixtures: синтетические события, генерация детерминированная (seed).

## 11. Развёртывание и окружения

- Одно окружение: **dev** (локальный docker-compose с RedPanda).
- Миграции/seed: seed-генерация событий в producer (детерминированный seed).

## 12. Безопасность

- Аутентификация/авторизация: нет (локальный RedPanda без SASL).
- Секреты: отсутствуют. Конфиг (bootstrap URL) — из env/файла.
- Чувствительные данные: нет, данные синтетические.

## 13. Мониторинг и observability

- Логи: структурные (slog) в stdout.
- Метрики: счётчики находок по типам в отчёте (report).
- Алерты/трейсинг: нет (demo).

## 14. Commands

### Default (root)
TEST_COMMAND: "go test ./..."
BUILD_COMMAND: "go build ./..."
E2E_COMMAND: "make demo"
LINT_COMMAND: "golangci-lint run"
