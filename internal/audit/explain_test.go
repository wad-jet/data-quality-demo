package audit

import (
	"strings"
	"testing"
)

func verdictBase() Report {
	return Report{
		PerTag: []TagMetric{{Tag: "missing", Total: 2, Caught: 2, Recall: 1.0, Findings: 2, FalsePos: 0, Precision: 1.0},
			{Tag: "dup", Total: 3, Caught: 3, Recall: 1.0, Findings: 3, FalsePos: 0, Precision: 1.0}},
		DLQ: DLQ{Count: 5, ByReason: map[string]int{"field_missing": 2, "invalid_json": 3}},
	}
}

func TestVerdictHealthy(t *testing.T) {
	healthy, problems := Verdict(verdictBase())
	if !healthy || len(problems) != 0 {
		t.Fatalf("want healthy/0, got %v %v", healthy, problems)
	}
}

func TestVerdictRecallProblem(t *testing.T) {
	r := verdictBase()
	r.PerTag[0].Caught = 1 // 1/2
	healthy, problems := Verdict(r)
	if healthy || len(problems) != 1 || !strings.Contains(problems[0], "1/2") {
		t.Fatalf("got %v %v", healthy, problems)
	}
}

func TestVerdictDLQMismatch(t *testing.T) {
	r := verdictBase()
	r.DLQTopic = &DLQTopic{Count: 4, ByReason: map[string]int{"field_missing": 2, "invalid_json": 2}}
	healthy, problems := Verdict(r)
	if healthy || len(problems) != 1 || !strings.Contains(problems[0], "DLQ") {
		t.Fatalf("got %v %v", healthy, problems)
	}
}

func TestVerdictDLQDedup(t *testing.T) {
	r := verdictBase()
	r.DLQTopic = &DLQTopic{Count: 4, ByReason: map[string]int{"field_missing": 2, "invalid_json": 2}}
	r.Warnings = []string{"DLQ-сверка: расхождение (ожид. 5, факт 4)"}
	_, problems := Verdict(r)
	if len(problems) != 1 {
		t.Fatalf("want exactly 1 problem (dedup), got %v", problems)
	}
}

func TestVerdictDLQWarningKept(t *testing.T) {
	r := verdictBase() // DLQTopic == nil — DLQ-проблемы нет
	r.Warnings = []string{"DLQ-сверка: расхождение (ожид. 5, факт 4)"}
	healthy, problems := Verdict(r)
	if healthy || len(problems) != 1 || problems[0] != "DLQ-сверка: расхождение (ожид. 5, факт 4)" {
		t.Fatalf("warning must be kept, got %v %v", healthy, problems)
	}
}

func TestVerdictByReasonMismatch(t *testing.T) {
	r := verdictBase()
	r.DLQTopic = &DLQTopic{Count: 5, ByReason: map[string]int{"field_missing": 3, "invalid_json": 2}} // Count==, by_reason !=
	healthy, problems := Verdict(r)
	if healthy || len(problems) != 1 {
		t.Fatalf("got %v %v", healthy, problems)
	}
}

func TestVerdictWarningsListed(t *testing.T) {
	r := verdictBase()
	r.Warnings = []string{"some warning"}
	_, problems := Verdict(r)
	if len(problems) != 1 || problems[0] != "some warning" {
		t.Fatalf("got %v", problems)
	}
}

func TestTimelineClusters(t *testing.T) {
	buckets := []TimelineBucket{{BucketS: 100, Count: 3}, {BucketS: 101, Count: 2}, {BucketS: 200, Count: 5}}
	got := TimelineClusters(buckets)
	if len(got) != 2 {
		t.Fatalf("want 2 clusters, got %v", got)
	}
	if got[0].StartS != 100 || got[0].EndS != 101 || got[0].Total != 5 {
		t.Fatalf("cluster 0: %v", got[0])
	}
	if got[1].StartS != 200 || got[1].Total != 5 {
		t.Fatalf("cluster 1: %v", got[1])
	}
}

func TestTimelineClustersBoundary60s(t *testing.T) {
	// разрыв ровно 60с — ОДИН кластер (правило: > 60)
	got := TimelineClusters([]TimelineBucket{{BucketS: 100, Count: 1}, {BucketS: 160, Count: 1}})
	if len(got) != 1 || got[0].Total != 2 {
		t.Fatalf("want 1 cluster at exactly 60s gap, got %v", got)
	}
}

func TestTimelineClustersEmpty(t *testing.T) {
	if got := TimelineClusters(nil); len(got) != 0 {
		t.Fatalf("want 0 clusters, got %v", got)
	}
}

func TestBarWidth(t *testing.T) {
	if w := BarWidth(10, 10); w != 40 {
		t.Fatalf("max cluster: %d", w)
	}
	if w := BarWidth(5, 10); w != 20 {
		t.Fatalf("half: %d", w)
	}
	if w := BarWidth(1, 4000); w != 1 {
		t.Fatalf("min: %d", w)
	}
}

func TestBarWidthZero(t *testing.T) {
	if w := BarWidth(0, 0); w != 1 {
		t.Fatalf("zero: %d", w)
	}
}

func TestPct(t *testing.T) {
	if got := Pct(2.0 / 3.0); got != "66.7%" {
		t.Fatalf("2/3: %q", got)
	}
	if got := Pct(1.0); got != "100.0%" {
		t.Fatalf("1: %q", got)
	}
}

func TestTagDescription(t *testing.T) {
	for _, tag := range []string{"missing", "dup", "typedrift", "ooo", "lag", "invalidjson"} {
		if TagDescription(tag) == "" || TagDescription(tag) == tag {
			t.Fatalf("tag %s: empty or passthrough", tag)
		}
	}
	if got := TagDescription("unknown_tag"); got != "unknown_tag" {
		t.Fatalf("unknown: %q", got)
	}
}
