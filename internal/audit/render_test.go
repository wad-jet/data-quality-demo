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
			Tag: "ooo", Check: "out_of_order", Total: 2, Caught: 1, Recall: 0.5,
			Findings: 3, FalsePos: 1, Precision: 0.6667,
		}},
		DLQ:      DLQ{Count: 1, ByReason: map[string]int{"field_missing": 1}},
		Timeline: []TimelineBucket{{BucketS: 1758621600, Count: 2}, {BucketS: 1758621700, Count: 2}},
		Overall:  Overall{TotalDefects: 3, Caught: 2, Recall: 0.6667, Findings: 4, FalsePos: 1, Precision: 0.75},
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
	md, err := RenderMarkdown(sampleReportFull())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, m := range []string{
		"# Отчёт о качестве данных (audit)",
		"**Вердикт: обнаружены проблемы (2).**",
		"## Что проверяли",
		"Инспектор (consumer — читает каждое событие и фиксирует нарушения) — записей всего 4:",
		"- 2 — заложенные дефекты (найдено 2 из 3),",
		"- 1 — дополнительные записи на те же дефекты (повторные отправки),",
		"- 1 — ложные срабатывания (не подтвердились при сверке с ledger).",
		"Подтвердились записи: 3 из 4 — это и есть Precision.",
		"| Метрика | Значение | Что это значит |",
		"| Recall | 2/3 (66.7%) |",
		"| Precision | 3/4 (75.0%) |",
		"## Дефекты по видам",
		"| Дефект | Что это | Заложено | Найдено из заложенных | Recall | Всего записей | Ложных | Precision |",
		"заказ без обязательного поля (например, суммы) — корректно обработать его нельзя",
		"Событие с «старой» отметкой времени (lag) приходит и не по порядку",
		"**Как читать колонки:**",
		"- **Заложено** — сколько дефектов этого вида producer вживил намеренно (записи ledger).",
		"- **Найдено из заложенных** — сколько из них инспектор нашёл (Заложено = Найдено → Recall 100%).",
		"- **Всего записей** — все записи инспектора этого вида. Запись — на сообщение, а «заложено/найдено» — на дефект: один дефект может дать несколько записей (повторная отправка сообщения).",
		"- **Ложных** — записи, не подтвердившиеся при сверке с ledger.",
		"## DLQ (dead-letter queue) — очередь проблемных сообщений",
		"Должно быть: 1 = 1 (missing) (по находкам инспектора, без обращения к брокеру)",
		"Фактически: не считалось (требуется запущенный брокер и флаг `-dlq-topic`)",
		"В DLQ попадают только нарушения схемы (missing, typedrift, invalidjson); dup, ooo и lag — валидные сообщения, остаются в основном топике и фиксируются только записями инспектора.",
		"## Таймлайн",
		"по **времени события** — отметке в самом событии, а не моменту получения",
		"Всего записей: 4 — это все записи из раздела «Что проверяли».",
		"разрыв 100с — событий с таким временем события не было",
		"## Как проверять отчёт за 10 секунд",
		"3. Precision = 100% у всех видов, кроме ooo (и иногда lag): у них ниже 100% — ожидаемо",
		"4. Сошлись пункты 1–3 и в отчёте нет предупреждений",
	} {
		if !strings.Contains(md, m) {
			t.Fatalf("MD missing %q", m)
		}
	}
}

func TestRenderMarkdownAllFound(t *testing.T) {
	r := Report{
		GeneratedAt:   time.Now().UTC(),
		Inputs:        Inputs{Ledger: "l.jsonl", Findings: "f.jsonl"},
		LedgerEntries: 1,
		PerTag: []TagMetric{{
			Tag: "missing", Check: "field_missing", Total: 1, Caught: 1, Recall: 1.0,
			Findings: 1, FalsePos: 0, Precision: 1.0,
		}},
		DLQ:      DLQ{Count: 0, ByReason: map[string]int{}},
		Timeline: []TimelineBucket{{BucketS: 100, Count: 1}},
		Overall:  Overall{TotalDefects: 1, Caught: 1, Recall: 1.0, Findings: 1, FalsePos: 0, Precision: 1.0},
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, m := range []string{
		"- 1 — заложенные дефекты (найдены все),",
		"Должно быть: 0 (по находкам инспектора, без обращения к брокеру)",
	} {
		if !strings.Contains(md, m) {
			t.Fatalf("allfound: missing %q", m)
		}
	}
	if strings.Contains(md, "дополнительные записи") {
		t.Fatalf("K==0: additional-records line must be absent: %s", md)
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
	if !strings.Contains(mism, "Статус: РАСХОЖДЕНИЕ") {
		t.Fatalf("mismatch: %s", mism)
	}
	if !strings.Contains(match, "В DLQ попадают только нарушения схемы") {
		t.Fatalf("dlq sentence: %s", match)
	}
}

func TestRenderMarkdownTimeline(t *testing.T) {
	r := sampleReportFull()
	r.Timeline = []TimelineBucket{{BucketS: 100, Count: 3}, {BucketS: 101, Count: 2}, {BucketS: 200, Count: 5}}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, m := range []string{
		"## Таймлайн",
		"Всего записей: 10 — это все записи из раздела «Что проверяли».",
		"разрыв 99с — событий с таким временем события не было: так проявляется дефект «лаг»",
		"несут «прошлую» отметку времени, и между ними и свежими событиями образуется разрыв",
	} {
		if !strings.Contains(md, m) {
			t.Fatalf("timeline: missing %q", m)
		}
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
		Overall: Overall{},
		PerTag:  []TagMetric{{Tag: "missing", Check: "field_missing", Total: 0, Caught: 0, Findings: 0}},
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	for _, m := range []string{
		"Записей инспектора (consumer — читает каждое событие и фиксирует нарушения) нет.",
		"| missing | заказ без обязательного поля (например, суммы) — корректно обработать его нельзя | 0 | 0 | — | 0 | 0 | — |",
	} {
		if !strings.Contains(md, m) {
			t.Fatalf("empty: missing %q", m)
		}
	}
	if strings.Contains(md, "## Таймлайн") {
		t.Fatalf("empty: timeline section must be absent")
	}
}

func TestRenderMarkdownNoOooNoteForOtherTags(t *testing.T) {
	r := sampleReportFull()
	for i := range r.PerTag {
		r.PerTag[i].Tag = "missing"
		r.PerTag[i].FalsePos = 3
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(md, "Событие с «старой» отметкой времени (lag) приходит и не по порядку") {
		t.Fatalf("note must not appear for non-ooo/lag tags")
	}
}
