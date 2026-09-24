package audit

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleReportFull() Report {
	return Report{
		GeneratedAt:   time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
		Inputs:        Inputs{Ledger: "ledger.jsonl", Findings: "findings.jsonl"},
		LedgerEntries: 5,
		PerTag: []TagMetric{{
			Tag: "missing", Check: "field_missing", Total: 1, Caught: 1, Recall: 1.0,
			Findings: 1, FalsePos: 0, Precision: 1.0,
		}, {
			Tag: "dup", Check: "duplicate", Total: 2, Caught: 1, Recall: 0.5,
			Findings: 2, FalsePos: 1, Precision: 0.5,
		}},
		DLQ:      DLQ{Count: 1, ByReason: map[string]int{"field_missing": 1}},
		Timeline: []TimelineBucket{{BucketS: 1758621600, Count: 1}},
		Overall:  Overall{TotalDefects: 3, Caught: 2, Recall: 0.6667, Findings: 3, FalsePos: 1, Precision: 0.6667},
		Warnings: []string{"sample warning"},
	}
}

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	rep := sampleReportFull()
	if err := WriteJSON(rep, path); err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := LoadJSON(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// compare full report using DeepEqual
	if !reflect.DeepEqual(back, rep) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", back, rep)
	}
	// ensure ledger_entries field is present in JSON file
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "ledger_entries") {
		t.Fatalf("ledger_entries not serialized")
	}
}

// TestLoadJSON error cases
func TestLoadJSONSyntaxError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	// write invalid JSON
	os.WriteFile(path, []byte("{invalid json}"), 0o644)
	_, err := LoadJSON(path)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), ":") {
		t.Fatalf("error does not contain path and location: %v", err)
	}
}

func TestLoadJSONTypeError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "type.json")
	content := `{
        "generated_at": "2026-09-23T10:00:00Z",
        "inputs": {"ledger": "", "findings": ""},
        "overall": {"recall": "oops", "precision": 0.0, "caught": 0, "findings": 0, "false_positives": 0, "total_defects": 0},
        "per_tag": [], "dlq": {"count":0,"by_reason":{}}, "timeline": [], "ledger_entries": 0
    }`
	os.WriteFile(path, []byte(content), 0o644)
	_, err := LoadJSON(path)
	if err == nil {
		t.Fatalf("expected type error")
	}
	if !strings.Contains(err.Error(), "field overall.recall") && !strings.Contains(err.Error(), "field overall") {
		t.Fatalf("error does not mention field name: %v", err)
	}
}

func TestRenderMarkdown(t *testing.T) {
	r := sampleReportFull() // Overall: Findings 3, FalsePos 1 (у dup) → precision (3-1)/3 = 66.7%
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, m := range []string{
		"# Отчёт о качестве данных (audit)",
		"**Вердикт: обнаружены проблемы (2).**", // dup recall 1/2 + "sample warning"
		"## Что проверяли",
		"## Метрики простыми словами",
		"| Recall | 2/3 (66.7%) |",
		"| Precision | 2/3 (66.7%) |",
		"## Дефекты по видам",
		"| missing | заказ без обязательного поля (например, суммы) — корректно обработать его нельзя |",
		"## DLQ — очередь проблемных сообщений",
		"Должно быть: 1 (по находкам инспектора, offline)",
		"Фактически: не считалось (требуется запущенный брокер и флаг `-dlq-topic`)",
		"## Как проверять отчёт за 10 секунд",
		"audit-report.json",
		"`-format text`",
	} {
		if !strings.Contains(md, m) {
			t.Fatalf("MD missing %q in:\n%s", m, md)
		}
	}
}

// dlqTopicReport returns sampleReportFull with a DLQTopic attached.
// match=false makes the topic count differ from the offline DLQ (mismatch).
func dlqTopicReport(match bool) Report {
	rep := sampleReportFull()
	count := rep.DLQ.Count
	if !match {
		count += 1
	}
	rep.DLQTopic = &DLQTopic{Count: count, ByReason: rep.DLQ.ByReason}
	return rep
}

func TestRenderMarkdownDLQTopic(t *testing.T) {
	match, err := RenderMarkdown(dlqTopicReport(true))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(match, "Статус: совпадает") {
		t.Fatalf("match: %s", match)
	}
	mism, err := RenderMarkdown(dlqTopicReport(false))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(mism, "Статус: РАСХОЖДЕНИЕ") || !strings.Contains(mism, "ожид.") {
		t.Fatalf("mismatch details in-place: %s", mism)
	}
	// DLQTopic == nil
	base, _ := RenderMarkdown(sampleReportFull())
	if !strings.Contains(base, "Фактически: не считалось") {
		t.Fatalf("nil topic: %s", base)
	}
}

func TestRenderMarkdownTimeline(t *testing.T) {
	r := sampleReportFull()
	r.Timeline = []TimelineBucket{{BucketS: 100, Count: 3}, {BucketS: 101, Count: 2}, {BucketS: 200, Count: 5}}
	md, _ := RenderMarkdown(r)
	if !strings.Contains(md, "## Таймлайн") ||
		!strings.Contains(md, "разрыв 99с") ||
		!strings.Contains(md, "дефект «лаг»") {
		t.Fatalf("timeline: %s", md)
	}
	// пустой таймлайн → секции нет
	r.Timeline = nil
	md2, _ := RenderMarkdown(r)
	if strings.Contains(md2, "## Таймлайн") {
		t.Fatalf("empty timeline section must be omitted: %s", md2)
	}
}

func TestRenderHTML(t *testing.T) {
	html, err := RenderHTML(sampleReportFull()) // problems: dup recall + warning
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, m := range []string{
		"<h1>Отчёт о качестве данных (audit)</h1>",
		`class="verdict-bad"`,
		"<h2>Что проверяли</h2>",
		"<h2>Метрики простыми словами</h2>",
		"<h2>Дефекты по видам</h2>",
		"заказ без обязательного поля",
		`class="num metric-bad"`,  // dup: Caught 1 < Total 2
		`class="num metric-ok"`,   // missing: recall 100%
		`class="num metric-warn"`, // dup: precision 50%
		"<h2>DLQ — очередь проблемных сообщений</h2>",
		"Фактически: не считалось",
		"Как проверять отчёт за 10 секунд",
	} {
		if !strings.Contains(html, m) {
			t.Fatalf("HTML missing %q", m)
		}
	}
}

func TestRenderHTMLDLQTopic(t *testing.T) {
	match, err := RenderHTML(dlqTopicReport(true))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(match, "Статус: совпадает") {
		t.Fatalf("match: %s", match)
	}
	mism, err := RenderHTML(dlqTopicReport(false))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(mism, "Статус: РАСХОЖДЕНИЕ") ||
		!strings.Contains(mism, `class="metric-bad mismatch"`) {
		t.Fatalf("mismatch: %s", mism)
	}
}

func TestRenderHTMLTimeline(t *testing.T) {
	r := sampleReportFull()
	r.Timeline = []TimelineBucket{{BucketS: 100, Count: 3}, {BucketS: 200, Count: 5}}
	html, err := RenderHTML(r)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(html, "<h2>Таймлайн</h2>") ||
		!strings.Contains(html, `class="tl-bar"`) ||
		!strings.Contains(html, "разрыв 100с") {
		t.Fatalf("timeline: %s", html)
	}
	if !strings.Contains(html, `style="width: 60%"`) || !strings.Contains(html, `style="width: 100%"`) {
		t.Fatalf("bar widths (60%%, 100%%) missing: %s", html)
	}
}

func TestRenderHTMLZeroCountTimeline(t *testing.T) {
	r := sampleReportFull()
	r.Timeline = []TimelineBucket{{BucketS: 100, Count: 0}, {BucketS: 101, Count: 0}}
	html, err := RenderHTML(r) // maxSum == 0 — не должно паниковать
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(html, `style="width: 1%"`) {
		t.Fatalf("zero-count bar width: %s", html)
	}
}

func TestRenderMarkdownEmptyReport(t *testing.T) {
	r := Report{
		Overall: Overall{}, // всё нулевое, пустой таймлайн
		PerTag:  []TagMetric{{Tag: "missing", Total: 0, Caught: 0, Findings: 0}},
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	if !strings.Contains(md, "**Вердикт: проблем не обнаружено.**") {
		t.Fatalf("empty must be healthy: %s", md)
	}
	if strings.Contains(md, "## Таймлайн") {
		t.Fatalf("no timeline section: %s", md)
	}
	// тег с Total==0 — recall «—», а не 0.0%
	if !strings.Contains(md, "| missing | заказ без обязательного поля (например, суммы) — корректно обработать его нельзя | 0 | 0 | — | 0 | 0 | — |") {
		t.Fatalf("Total==0 row must show «—»: %s", md)
	}
}

func TestRenderMarkdownNoOooNoteForOtherTags(t *testing.T) {
	r := sampleReportFull()
	r.PerTag = []TagMetric{{Tag: "missing", Total: 2, Caught: 2, Recall: 1.0, Findings: 3, FalsePos: 1, Precision: 0.6667}}
	md, _ := RenderMarkdown(r)
	if strings.Contains(md, "одна причина, две записи") {
		t.Fatalf("note must be ooo/lag only: %s", md)
	}
	r.PerTag = append(r.PerTag, TagMetric{Tag: "ooo", Total: 1, Caught: 1, Recall: 1.0, Findings: 2, FalsePos: 1, Precision: 0.5})
	md2, _ := RenderMarkdown(r)
	if !strings.Contains(md2, "одна причина, две записи") {
		t.Fatalf("ooo fp>0 note expected: %s", md2)
	}
}
