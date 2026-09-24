package audit

import (
	"encoding/json"
	"github.com/twmb/franz-go/pkg/kgo"
	"strings"
	"testing"
)

func TestParseDLQReason(t *testing.T) {
	// Header present
	rec := kgo.Record{Headers: []kgo.RecordHeader{{Key: "dq.reason", Value: []byte("field_missing")}}}
	reason, missing := parseDLQReason(rec.Headers)
	if missing || reason != "field_missing" {
		t.Fatalf("expected present reason, got %v, missing=%v", reason, missing)
	}
	// Header absent
	rec2 := kgo.Record{Headers: []kgo.RecordHeader{{Key: "other", Value: []byte("v")}}}
	reason2, missing2 := parseDLQReason(rec2.Headers)
	if !missing2 || reason2 != "unknown" {
		t.Fatalf("expected unknown reason, got %v, missing=%v", reason2, missing2)
	}
}

func TestCheckDLQTopic(t *testing.T) {
	expected := DLQ{Count: 2, ByReason: map[string]int{"field_missing": 1, "type_drift": 1}}
	actualMatch := DLQTopic{Count: 2, ByReason: map[string]int{"type_drift": 1, "field_missing": 1}}
	if s := CheckDLQTopic(expected, actualMatch); s != "" {
		t.Fatalf("expected match, got warning %s", s)
	}
	actualCountDiff := DLQTopic{Count: 1, ByReason: map[string]int{"field_missing": 1, "type_drift": 1}}
	if s := CheckDLQTopic(expected, actualCountDiff); s == "" {
		t.Fatalf("expected mismatch on count")
	}
	actualReasonDiff := DLQTopic{Count: 2, ByReason: map[string]int{"field_missing": 2}}
	if s := CheckDLQTopic(expected, actualReasonDiff); s == "" {
		t.Fatalf("expected mismatch on reasons")
	}
}

func TestRenderHumanWithDLQTopic(t *testing.T) {
	rep := sampleReport()
	// Attach DLQTopic matching DLQ
	rep.DLQTopic = &DLQTopic{Count: rep.DLQ.Count, ByReason: rep.DLQ.ByReason}
	out := rep.RenderHuman()
	if !strings.Contains(out, "DLQ (topic):") {
		t.Fatalf("missing DLQ (topic) line: %s", out)
	}
	if !strings.Contains(out, "совпадает с findings") {
		t.Fatalf("expected match indicator: %s", out)
	}
	// Mismatch case
	rep2 := sampleReport()
	rep2.DLQTopic = &DLQTopic{Count: rep2.DLQ.Count + 1, ByReason: rep2.DLQ.ByReason}
	out2 := rep2.RenderHuman()
	if !strings.Contains(out2, "РАСХОЖДЕНИЕ") {
		t.Fatalf("expected mismatch indicator: %s", out2)
	}
}

func TestJSONDLQTopicField(t *testing.T) {
	rep := sampleReport()
	rep.DLQTopic = &DLQTopic{Count: 5, ByReason: map[string]int{"invalid_json": 5}}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if !strings.Contains(string(data), "dlq_topic") {
		t.Fatalf("JSON missing dlq_topic field: %s", string(data))
	}
}
