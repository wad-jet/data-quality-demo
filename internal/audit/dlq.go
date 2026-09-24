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
	metaResp, err := metaReq.RequestWith(ctx, client)
	if err != nil {
		return DLQTopic{}, nil, fmt.Errorf("audit: dlq: %w", err)
	}
	if len(metaResp.Topics) == 0 {
		return DLQTopic{}, nil, fmt.Errorf("audit: dlq: metadata empty response for %s", topic)
	}
	// Verify no error code and collect partitions.
	var partitions []int32
	for _, t := range metaResp.Topics {
		if t.ErrorCode != 0 {
			if perr := kerr.ErrorForCode(t.ErrorCode); perr != nil {
				return DLQTopic{}, nil, perr
			}
			return DLQTopic{}, nil, fmt.Errorf("audit: dlq: metadata error code %d", t.ErrorCode)
		}
		for _, p := range t.Partitions {
			partitions = append(partitions, p.Partition)
		}
	}

	// ---------- Precheck: list offsets (high watermarks) ----------
	loReq := kmsg.NewPtrListOffsetsRequest()
	// Build a single topic entry with all partitions.
	loReq.Topics = []kmsg.ListOffsetsRequestTopic{{
		Topic: topic,
		Partitions: func() []kmsg.ListOffsetsRequestTopicPartition {
			ps := make([]kmsg.ListOffsetsRequestTopicPartition, len(partitions))
			for i, p := range partitions {
				ps[i] = kmsg.ListOffsetsRequestTopicPartition{Partition: p, Timestamp: -1}
			}
			return ps
		}(),
	}}
	loResp, err := loReq.RequestWith(ctx, client)
	if err != nil {
		return DLQTopic{}, nil, fmt.Errorf("audit: dlq: %w", err)
	}
	endOffsets := map[int32]int64{}
	for _, t := range loResp.Topics {
		for _, p := range t.Partitions {
			if p.ErrorCode != 0 {
				if perr := kerr.ErrorForCode(p.ErrorCode); perr != nil {
					return DLQTopic{}, nil, perr
				}
				return DLQTopic{}, nil, fmt.Errorf("audit: dlq: listoffsets error code %d", p.ErrorCode)
			}
			endOffsets[p.Partition] = p.Offset
		}
	}

	// If no partitions, nothing to read.
	if len(partitions) == 0 {
		return DLQTopic{Count: 0, ByReason: map[string]int{}}, nil, nil
	}

	// Initialise counters and tracking structures.
	result := DLQTopic{Count: 0, ByReason: map[string]int{}}
	warnings := []string{}
	lastSeen := map[int32]int64{}
	for _, p := range partitions {
		lastSeen[p] = -1 // ensures uniform stop‑condition logic.
	}

	// ---------- Consumption loop ----------
	timer := time.NewTimer(dlqReadTimeout)
	defer timer.Stop()
	done := false
	for !done {
		select {
		case <-ctx.Done():
			// Context cancelled – return partial result with the cancellation error.
			return result, warnings, ctx.Err()
		case <-timer.C:
			// Timeout cap – emit warning and stop.
			warnings = append(warnings, "DLQ-чтение: timeout, возможно неполно")
			done = true
			continue
		default:
		}

		fetches := client.PollFetches(ctx)
		if fetches == nil {
			if ctx.Err() != nil {
				return result, warnings, ctx.Err()
			}
			continue
		}
		fetches.EachRecord(func(r *kgo.Record) {
			result.Count++
			reason, missing := parseDLQReason(r.Headers)
			result.ByReason[reason]++
			if missing {
				warnings = append(warnings, "DLQ: missing reason header")
			}
			// Update last seen offset for the partition.
			if r.Offset > lastSeen[r.Partition] {
				lastSeen[r.Partition] = r.Offset
			}
		})

		// Evaluate stop condition across all partitions.
		allDone := true
		for _, p := range partitions {
			if lastSeen[p]+1 < endOffsets[p] {
				allDone = false
				break
			}
		}
		if allDone {
			done = true
		}
	}

	return result, warnings, nil
}
