package audit

import (
    "context"
    "fmt"
    "github.com/twmb/franz-go/pkg/kgo"
)

// DLQTopic represents DLQ consumption statistics.
type DLQTopic struct {
    Count    int            `json:"count"`
    ByReason map[string]int `json:"by_reason"`
}



// parseDLQReason extracts the reason from record headers.
// Returns reason and a flag indicating whether the header was missing.
func parseDLQReason(headers []kgo.RecordHeader) (string, bool) {
    const reasonKey = "dq.reason"
    for _, h := range headers {
        if string(h.Key) == reasonKey {
            return string(h.Value), false
        }
    }
    return "unknown", true
}

// checkDLQTopic compares expected DLQ (from schema violations) with actual consumed DLQTopic.
// Returns empty string on match, otherwise a warning string.
func checkDLQTopic(expected DLQ, actual DLQTopic) string {
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
    // Stub implementation for unit tests. Real broker consumption is out of scope for this task.
    // Returns empty counters and no warnings.
    return DLQTopic{Count: 0, ByReason: map[string]int{}}, nil, nil
}
