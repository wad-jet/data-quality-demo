package audit

import (
	"context"
	"fmt"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"time"
)

const (
	reasonKey      = "dq.reason"
	dlqReadTimeout = 15 * time.Second
	// dlqIdleStop is the "caught up" window: the DLQ is static (the consumer
	// shuts down before audit runs), so once no new records arrive for this
	// long we consider the topic fully read. This replaces the ListOffsets
	// end-offset stop condition, which is broken on Redpanda v26.2.3
	// (raw kmsg ListOffsets always returns FENCED_LEADER_EPOCH there).
	dlqIdleStop = 2 * time.Second
)

// DLQTopic represents DLQ consumption statistics.
type DLQTopic struct {
	Count    int            `json:"count"`
	ByReason map[string]int `json:"by_reason"`
}

// parseDLQReason extracts the reason from record headers.
// Returns reason and a flag indicating whether the header was missing.
func parseDLQReason(headers []kgo.RecordHeader) (string, bool) {
	for _, h := range headers {
		if string(h.Key) == reasonKey {
			return string(h.Value), false
		}
	}
	// missing reason header
	return "unknown", true
}

// CheckDLQTopic compares expected DLQ (from schema violations) with actual consumed DLQTopic.
// Returns empty string on match, otherwise a warning string.
func CheckDLQTopic(expected DLQ, actual DLQTopic) string {
	if expected.Count != actual.Count {
		return fmt.Sprintf("DLQ-сверка: расхождение (ожид. %d, факт %d)", expected.Count, actual.Count)
	}
	// compare ByReason maps
	if len(expected.ByReason) != len(actual.ByReason) {
		return "DLQ-сверка: расхождение (by_reason count mismatch)"
	}
	for k, ev := range expected.ByReason {
		if av, ok := actual.ByReason[k]; !ok || av != ev {
			return fmt.Sprintf("DLQ-сверка: расхождение (reason %s ожид. %d, факт %d)", k, ev, av)
		}
	}
	return ""
}

// ConsumeDLQ reads DLQ messages from the given topic without a consumer group.
// Returns the aggregated DLQTopic, any warnings, and an error if precheck fails.
func ConsumeDLQ(ctx context.Context, bootstrap, topic string) (DLQTopic, []string, error) {
	// Initialize consumer client without a consumer group.
	client, err := kgo.NewClient(
		kgo.SeedBrokers(bootstrap),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchIsolationLevel(kgo.ReadUncommitted()),
	)
	if err != nil {
		return DLQTopic{}, nil, err
	}
	defer client.Close()

	// ---------- Precheck: metadata ----------
	metaReq := kmsg.NewPtrMetadataRequest()
	topicPtr := topic
	metaReq.Topics = []kmsg.MetadataRequestTopic{{Topic: &topicPtr}}
	// Errors are returned without an "audit: dlq:" prefix — the caller
	// (cmd/audit) adds that single prefix before printing to stderr.
	metaResp, err := metaReq.RequestWith(ctx, client)
	if err != nil {
		return DLQTopic{}, nil, err
	}
	if len(metaResp.Topics) == 0 {
		return DLQTopic{}, nil, fmt.Errorf("metadata empty response for %s", topic)
	}
	// Verify no error code (fast-fail on UNKNOWN_TOPIC_OR_PARTITION etc.).
	for _, t := range metaResp.Topics {
		if t.ErrorCode != 0 {
			if perr := kerr.ErrorForCode(t.ErrorCode); perr != nil {
				return DLQTopic{}, nil, perr
			}
			return DLQTopic{}, nil, fmt.Errorf("metadata error code %d", t.ErrorCode)
		}
	}

	// ---------- Consumption loop (idle-stop) ----------
	// The DLQ topic is static: the consumer has fully drained and shut down
	// before audit starts, so no new records appear. We read until no new
	// records arrive for dlqIdleStop, capped by dlqReadTimeout. This works for
	// both empty (stops after one idle window, count=0) and non-empty topics,
	// and avoids the ListOffsets precheck that Redpanda v26.2.3 rejects with
	// FENCED_LEADER_EPOCH.
	result := DLQTopic{Count: 0, ByReason: map[string]int{}}
	warnings := []string{}
	missingHeader := 0
	start := time.Now()
	for {
		if time.Since(start) >= dlqReadTimeout {
			warnings = append(warnings, "DLQ-чтение: timeout, возможно неполно")
			break
		}
		wait := dlqIdleStop
		if rem := dlqReadTimeout - time.Since(start); rem < wait {
			wait = rem
		}
		pctx, cancel := context.WithTimeout(ctx, wait)
		fetches := client.PollFetches(pctx)
		cancel()
		if fetches == nil {
			break
		}
		if ctx.Err() != nil {
			// Parent context cancelled — return partial result with the error.
			return result, warnings, ctx.Err()
		}
		got := 0
		fetches.EachRecord(func(r *kgo.Record) {
			got++
			result.Count++
			reason, missing := parseDLQReason(r.Headers)
			result.ByReason[reason]++
			if missing {
				missingHeader++
			}
		})
		if got == 0 {
			// Idle: no new records within the wait window — caught up (or empty).
			break
		}
	}
	if missingHeader > 0 {
		warnings = append(warnings, fmt.Sprintf("DLQ: %d messages without dq.reason header (bucketed as unknown)", missingHeader))
	}

	return result, warnings, nil
}
