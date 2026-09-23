# Makefile — one-command demo pipeline (producer -> RedPanda/Kafka -> consumer)
# Shell: /bin/sh (POSIX sh), macOS-compatible (nc, go, docker in PATH).

.DEFAULT_GOAL := demo

.PHONY: demo broker-up broker-down build test clean

BROKER_HOST := localhost
BROKER_PORT := 9092
BROKER_TIMEOUT := 60

# Full pipeline: broker up -> wait ready -> build -> producer -> consumer -> report -> broker down.
# Broker is torn down on EXIT/INT/TERM so Ctrl-C also stops the container.
demo:
	@cleanup() { docker compose down >/dev/null 2>&1 || true; }; \
	trap 'echo "[demo] interrupted - shutting down broker..."; cleanup; trap - EXIT; exit 130' INT; \
	trap 'echo "[demo] interrupted - shutting down broker..."; cleanup; trap - EXIT; exit 143' TERM; \
	trap 'cleanup' EXIT; \
	set -e; \
	echo "[demo] resetting demo artifacts (rm -f ledger.jsonl findings.jsonl)..."; \
	rm -f ledger.jsonl findings.jsonl; \
	echo "[demo] starting broker (docker compose up -d)..."; \
	docker compose up -d; \
	echo "[demo] waiting for broker at $(BROKER_HOST):$(BROKER_PORT) (up to $(BROKER_TIMEOUT)s)..."; \
	i=0; \
	while ! nc -z $(BROKER_HOST) $(BROKER_PORT) 2>/dev/null; do \
		i=$$((i + 1)); \
		if [ "$$i" -ge $(BROKER_TIMEOUT) ]; then \
			echo "[demo] ERROR: broker did not become ready at $(BROKER_HOST):$(BROKER_PORT) within $(BROKER_TIMEOUT)s" >&2; \
			exit 1; \
		fi; \
		sleep 1; \
	done; \
	echo "[demo] broker is up."; \
	$(MAKE) build; \
	echo "[demo] running producer (-count 1000 -rate 200 -seed 42 -ledger ledger.jsonl)..."; \
	./bin/producer -count 1000 -rate 200 -seed 42 -ledger ledger.jsonl; \
	echo "[demo] running consumer (-stop 1000 -ledger ledger.jsonl -findings findings.jsonl)..."; \
	./bin/consumer -stop 1000 -ledger ledger.jsonl -findings findings.jsonl; \
	echo ""; \
	echo "[demo] DONE. Full report above; findings written to: $(CURDIR)/findings.jsonl"

# Broker only: start
broker-up:
	docker compose up -d

# Broker only: stop
broker-down:
	docker compose down

# Build CLI binaries into bin/
build:
	go build -o bin/producer ./cmd/producer
	go build -o bin/consumer ./cmd/consumer

# Run all Go tests
test:
	go test ./...

# Remove build artifacts and demo outputs
clean:
	rm -rf bin
	rm -f ledger.jsonl producer-ledger.jsonl findings.jsonl *.log
