# Human-readable audit report (MD/HTML) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** MD/HTML-рендеры `audit-report.json` становятся русскими «человеческими» отчётами: вердикт, метрики простыми словами, легенда дефектов, человеческий таймлайн, чеклист.

**Architecture:** Новый `internal/audit/explain.go` — чистые функции (вердикт, легенда, кластеризация таймлайна, ширина бара) + все русские статические строки. `render.go` — структурные `RenderMarkdown`/`RenderHTML` по секциям spec §4. `text`-формат и JSON не трогаются.

**Tech Stack:** Go 1.26 (module `dqdemo`), стандартная библиотека только (fmt, strings, math, time).

**Spec:** `docs/superpowers/specs/2026-09-24-audit-report-readable-design.md`

## Global Constraints

- Язык пользовательских строк MD/HTML — **русский**; `text`/JSON форматы — **не меняются** (английский).
- Единственный источник истины для DLQ-расхождения — `CheckDLQTopic(expected DLQ, actual DLQTopic) string` (`internal/audit/dlq.go:43`): сверяет `Count` и `ByReason`; пустая строка = совпадает.
- Warnings с префиксом `DLQ-сверка` дедуплицируются в вердикте **только если** DLQ-проблема уже добавлена (spec §3.3).
- Кластер таймлайна: разрыв между соседними `bucket_s` **> 60с** → новый кластер (граница 60с — один кластер).
- Ширина ASCII-бара: `max(1, round(40 · total / maxTotal))`; HTML-полоса — `width` в % (макс. кластер = 100%).
- Теги с `Total == 0` — recall отображается «—»; `Findings == 0` — precision «—».
- Проценты — `Pct(v) = fmt.Sprintf("%.1f%%", v*100)` (одна цифра после точки, как сейчас).
- HTML: инлайн CSS, без JS, самодостаточный файл; классы: `verdict-ok`, `verdict-bad`, `metric-ok`, `metric-warn`, `metric-bad`, `mismatch` (последний сохраняется).
- HTML-экранирование отсутствует (pre-existing, осознанно вне scope — не вводить).
- TDD: каждый шаг-код начинается с failing-теста. Коммит на каждую задачу.

## Review Focus

1. **Внешний JSON с DLQ-мismatch по ByReason при равных Count** (Count==, by_reason !=) → ровно одна DLQ-проблема в вердикте, «РАСХОЖДЕНИЕ» и детали в DLQ-секции, красный маркер (тесты: Task 1 `TestVerdictByReasonMismatch`, Task 2 `TestRenderMarkdownDLQTopic`, Task 3 `TestRenderHTMLDLQTopic`).
2. **Отчёт без единого finding** (пустой `timeline`, все `Total==0`, `DLQ.Count==0`) → нет division-by-zero, «—» в таблице, секции Таймлайна нет, вердикт healthy (тесты: Task 1 `TestTimelineClustersEmpty`/`TestBarWidthZero`, Task 2 `TestRenderMarkdownEmptyReport`).
3. **FP у тега вне ooo/lag** (напр. `missing` с `FalsePos>0`) → пояснительный абзац НЕ выводится, но precision виден в таблице (тест: Task 2 `TestRenderMarkdownNoOooNoteForOtherTags`).
4. **`DLQTopic == nil`** → DLQ-проблемы в вердикте НЕТ, строка «Фактически: не считалось…», warning без DLQ-проблемы с префиксом «DLQ-сверка» сохраняется (тесты: Task 1 `TestVerdictDLQWarningKept`, Task 2 `TestRenderMarkdownDLQTopic`).
5. **Один кластер таймлайна / один бак** → одна строка, бар = 40 (MD) / 100% (HTML) (тесты: Task 1 `TestBarWidth`, Task 2 `TestRenderMarkdownTimeline`).

---

### Task 1: `explain.go` — вердикт, легенда, кластеры, бар

**Files:**
- Create: `internal/audit/explain.go`
- Test: `internal/audit/explain_test.go`

**Interfaces:**
- Consumes: `Report`, `TagMetric`, `DLQ`, `DLQTopic`, `TimelineBucket` (`audit.go`), `CheckDLQTopic` (`dlq.go:43`).
- Produces: `TagDescription(tag string) string`; `Verdict(r Report) (healthy bool, problems []string)`; `TimelineCluster{StartS, EndS int64; Total int}`; `TimelineClusters(buckets []TimelineBucket) []TimelineCluster` (вход — отсортированные по `BucketS`); `BarWidth(total, maxTotal int) int`; `Pct(v float64) string`.

- [ ] **Step 1: Failing-тесты (новые)**

`internal/audit/explain_test.go` (package `audit`):

```go
func verdictBase() Report {
	return Report{
		PerTag: []TagMetric{
			{Tag: "missing", Total: 2, Caught: 2, Recall: 1.0, Findings: 2, FalsePos: 0, Precision: 1.0},
			{Tag: "dup", Total: 3, Caught: 3, Recall: 1.0, Findings: 3, FalsePos: 0, Precision: 1.0},
		},
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
```

- [ ] **Step 2: Запустить — убедиться, что тесты падают**

Run: `go test ./internal/audit/ -run 'TestVerdict|TestTimelineClusters|TestBarWidth|TestPct|TestTagDescription' -count=1`
Expected: FAIL — «undefined: Verdict / TimelineClusters / BarWidth / Pct / TagDescription».

- [ ] **Step 3: Минимальная реализация**

`internal/audit/explain.go`:

```go
package audit

import (
	"fmt"
	"math"
	"strings"
)

// TagDescriptions — русская легенда дефектов (spec §5).
var TagDescriptions = map[string]string{
	"missing":     "заказ без обязательного поля",
	"dup":         "повторная отправка одного заказа",
	"typedrift":   "поле другого типа (число стало текстом и т.п.)",
	"ooo":         "события пришли не по порядку",
	"lag":         "«старое» событие (отметка времени в прошлом)",
	"invalidjson": "некорректный JSON — сообщение не удаётся прочитать",
}

// TagDescription — текст легенды; неизвестный тег возвращается как есть.
func TagDescription(tag string) string {
	if d, ok := TagDescriptions[tag]; ok {
		return d
	}
	return tag
}

// Verdict — детерминированный вердикт для шапки (spec §3).
func Verdict(r Report) (healthy bool, problems []string) {
	problems = []string{}
	for _, tm := range r.PerTag {
		if tm.Total > 0 && tm.Caught < tm.Total {
			problems = append(problems, fmt.Sprintf("не все дефекты вида %s найдены: найдено/всего = %d/%d", tm.Tag, tm.Caught, tm.Total))
		}
	}
	dlqProblem := false
	if r.DLQTopic != nil {
		if m := CheckDLQTopic(r.DLQ, *r.DLQTopic); m != "" {
			dlqProblem = true
			problems = append(problems, "DLQ: расхождение — "+strings.TrimPrefix(m, "DLQ-сверка: "))
		}
	}
	for _, w := range r.Warnings {
		if dlqProblem && strings.HasPrefix(w, "DLQ-сверка") {
			continue // один факт — одна проблема
		}
		problems = append(problems, w)
	}
	return len(problems) == 0, problems
}

// TimelineCluster — группа последовательных баков таймлайна.
type TimelineCluster struct {
	StartS int64
	EndS   int64
	Total  int
}

// TimelineClusters — разбивка на кластеры: разрыв > 60с → новый кластер (spec §4.6).
// Вход — баки, отсортированные по BucketS (так их строит BuildReport).
func TimelineClusters(buckets []TimelineBucket) []TimelineCluster {
	if len(buckets) == 0 {
		return nil
	}
	clusters := []TimelineCluster{{StartS: buckets[0].BucketS, EndS: buckets[0].BucketS, Total: buckets[0].Count}}
	for _, b := range buckets[1:] {
		last := &clusters[len(clusters)-1]
		if b.BucketS-last.EndS > 60 {
			clusters = append(clusters, TimelineCluster{StartS: b.BucketS, EndS: b.BucketS, Total: b.Count})
		} else {
			last.EndS = b.BucketS
			last.Total += b.Count
		}
	}
	return clusters
}

// BarWidth — ширина ASCII-бара: max(1, round(40·total/maxTotal)) (spec §4.6).
func BarWidth(total, maxTotal int) int {
	if maxTotal <= 0 {
		return 1
	}
	w := int(math.Round(40 * float64(total) / float64(maxTotal)))
	if w < 1 {
		w = 1
	}
	return w
}

// Pct — процент с одной цифрой после точки.
func Pct(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}
```

- [ ] **Step 4: Запустить — убедиться, что тесты зелёные**

Run: `go test ./internal/audit/ -count=1`
Expected: PASS (в т.ч. все старые тесты пакета).

- [ ] **Step 5: Коммит**

```bash
git add internal/audit/explain.go internal/audit/explain_test.go
git commit -m "feat(audit): explain.go — verdict, ru legend, timeline clusters, bar width"
```

---

### Task 2: `RenderMarkdown` — русский человекочитаемый отчёт

**Files:**
- Modify: `internal/audit/render.go` (функция `RenderMarkdown`, ~строки 40-105)
- Test: `internal/audit/render_test.go` (заменить `TestRenderMarkdown`, `TestRenderMarkdownDLQTopic`, `TestRenderMarkdownTimeline`; добавить `TestRenderMarkdownEmptyReport`, `TestRenderMarkdownNoOooNoteForOtherTags`)

**Interfaces:**
- Consumes: `Verdict`, `TagDescription`, `TimelineClusters`, `BarWidth`, `Pct` (Task 1); `CheckDLQTopic` (`dlq.go`); хелперы `sampleReportFull()`, `dlqTopicReport(match bool)` (уже в `render_test.go`).
- Produces: `RenderMarkdown(r Report) (string, error)` — та же сигнатура, новая структура вывода (spec §4).

- [ ] **Step 1: Заменить MD-тесты на failing**

В `internal/audit/render_test.go` **заменить** тела трёх тестов и добавить два новых:

```go
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
		"| missing | заказ без обязательного поля |",
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
```

```go
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
	if !strings.Contains(md, "| missing | заказ без обязательного поля | 0 | 0 | — | 0 | 0 | — |") {
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
```

В `TestRenderMarkdownTimeline`: `разрыв 99с` — по данным 100,101,200 (200−101=99).

- [ ] **Step 2: Запустить — убедиться, что тесты падают**

Run: `go test ./internal/audit/ -run 'TestRenderMarkdown' -count=1`
Expected: FAIL — текущий `RenderMarkdown` выдаёт старую английскую структуру.

- [ ] **Step 3: Реализовать `RenderMarkdown` заново**

В `internal/audit/render.go` заменить тело `RenderMarkdown` (сигнатура не меняется):

```go
// RenderMarkdown — русский «человеческий» MD-отчёт (spec §4).
func RenderMarkdown(r Report) (string, error) {
	var b strings.Builder
	b.WriteString("# Отчёт о качестве данных (audit)\n\n")
	healthy, problems := Verdict(r)
	if healthy {
		b.WriteString("**Вердикт: проблем не обнаружено.**\n\n")
		if r.DLQTopic != nil {
			b.WriteString("Все заложенные дефекты найдены; сверка DLQ — совпадает.\n\n")
		} else {
			b.WriteString("Все заложенные дефекты найдены.\n\n")
		}
	} else {
		fmt.Fprintf(&b, "**Вердикт: обнаружены проблемы (%d).**\n\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Что проверяли\n\n")
	fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Инспектор нашёл %d записей: %d — реальные дефекты, %d — ложные срабатывания.\n\n",
		r.LedgerEntries, r.Overall.TotalDefects, r.Overall.Findings, r.Overall.Caught, r.Overall.FalsePos)

	b.WriteString("## Метрики простыми словами\n\n")
	b.WriteString("| Метрика | Значение | Что это значит |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Recall | %d/%d (%s) | Доля заложенных дефектов, которые удалось найти |\n",
		r.Overall.Caught, r.Overall.TotalDefects, Pct(r.Overall.Recall))
	fmt.Fprintf(&b, "| Precision | %d/%d (%s) | Доля находок, которые подтвердились; остальные — ложные срабатывания |\n",
		r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings, Pct(r.Overall.Precision))
	b.WriteString("\n")

	b.WriteString("## Дефекты по видам\n\n")
	b.WriteString("| Дефект | Что это | Заложено | Найдено | Recall | Записей | Ложных | Precision |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, tm := range r.PerTag {
		recall, prec := "—", "—"
		if tm.Total > 0 {
			recall = Pct(tm.Recall)
		}
		if tm.Findings > 0 {
			prec = Pct(tm.Precision)
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %d | %d | %s |\n",
			tm.Tag, TagDescription(tm.Tag), tm.Total, tm.Caught, recall, tm.Findings, tm.FalsePos, prec)
	}
	b.WriteString("\n")
	for _, tm := range r.PerTag {
		if (tm.Tag == "ooo" || tm.Tag == "lag") && tm.FalsePos > 0 {
			b.WriteString("«Старое» событие (лаг) выглядит и как «не по порядку» — одна причина, две записи; это ожидаемое поведение демо, а не ошибка.\n\n")
			break
		}
	}

	b.WriteString("## DLQ — очередь проблемных сообщений\n\n")
	fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, offline)\n", r.DLQ.Count)
	if r.DLQTopic != nil {
		fmt.Fprintf(&b, "Фактически: %d (прочитано из брокера)\n", r.DLQTopic.Count)
		if m := CheckDLQTopic(r.DLQ, *r.DLQTopic); m == "" {
			b.WriteString("Статус: совпадает\n")
		} else {
			fmt.Fprintf(&b, "Статус: РАСХОЖДЕНИЕ — %s\n", strings.TrimPrefix(m, "DLQ-сверка: "))
		}
	} else {
		b.WriteString("Фактически: не считалось (требуется запущенный брокер и флаг `-dlq-topic`)\n")
	}
	b.WriteString("\n")

	if len(r.Timeline) > 0 {
		b.WriteString("## Таймлайн\n\n")
		clusters := TimelineClusters(r.Timeline)
		maxSum := 0
		for _, c := range clusters {
			if c.Total > maxSum {
				maxSum = c.Total
			}
		}
		for i, c := range clusters {
			if i > 0 {
				fmt.Fprintf(&b, "разрыв %ds — как правило, дефект «лаг»: события со «старой» отметкой времени\n\n", c.StartS-clusters[i-1].EndS)
			}
			fmt.Fprintf(&b, "%s–%s (время события) %s %d\n",
				utcHMS(c.StartS), utcHMS(c.EndS), strings.Repeat("█", BarWidth(c.Total, maxSum)), c.Total)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Как проверять отчёт за 10 секунд\n\n")
	b.WriteString("1. Recall = 100% по всем видам? — значит, ни один заложенный дефект не просочился.\n")
	b.WriteString("2. DLQ: «совпадает»? — значит, в очереди проблемных сообщений ничего не потеряно.\n")
	b.WriteString("3. Precision ниже 100% у ooo/lag — это ожидаемо (двойные срабатывания); у остальных видов — 100%.\n")
	b.WriteString("4. Вердикт в шапке сводит всё в одну строку.\n\n")

	fmt.Fprintf(&b, "---\n*Сгенерировано: %s · Данные: %s, %s*\n",
		r.GeneratedAt.UTC().Format("2006-01-02 15:04 (UTC)"), r.Inputs.Ledger, r.Inputs.Findings)
	b.WriteString("*Машиночитаемая версия: `audit-report.json`; формат для специалистов: `-format text`*\n")
	return b.String(), nil
}

func utcHMS(bucketS int64) string {
	return time.Unix(bucketS, 0).UTC().Format("15:04:05")
}
```

Проверить импорт `render.go`: `time` (уже есть, если нет — добавить); `fmt`, `strings` — есть.

- [ ] **Step 4: Запустить — MD-тесты зелёные + весь пакет**

Run: `go test ./internal/audit/ -count=1`
Expected: PASS. Если `TestRenderHumanWithDLQTopic`/JSON-тесты упали — `text`/JSON не должны были измениться: искать регресс.

- [ ] **Step 5: Коммит**

```bash
git add internal/audit/render.go internal/audit/render_test.go
git commit -m "feat(audit): RenderMarkdown — human-readable ru report (verdict, legend, timeline, checklist)"
```

---

### Task 3: `RenderHTML` — русский отчёт с цветовыми маркерами

**Files:**
- Modify: `internal/audit/render.go` (функция `RenderHTML`, ~строки 107-175; `<style>` блок)
- Test: `internal/audit/render_test.go` (заменить `TestRenderHTML`, `TestRenderHTMLDLQTopic`, `TestRenderHTMLTimeline`)

**Interfaces:**
- Consumes: то же, что Task 2 (`Verdict`, `TagDescription`, `TimelineClusters`, `Pct`, `utcHMS`, `CheckDLQTopic`).
- Produces: `RenderHTML(r Report) (string, error)` — самодостаточный HTML; классы `verdict-ok`, `verdict-bad`, `metric-ok`, `metric-warn`, `metric-bad`, `mismatch`.

- [ ] **Step 1: Заменить HTML-тесты на failing**

```go
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
		`class="metric-bad"`,  // dup: Caught 1 < Total 2
		`class="metric-ok"`,   // missing: recall 100%
		`class="metric-warn"`, // dup: precision 50%
		"<h2>DLQ — очередь проблемных сообщений</h2>",
		"Фактически: не считалось",
		"Как проверять отчёт за 10 секунд",
	} {
		if !strings.Contains(html, m) {
			t.Fatalf("HTML missing %q", m)
		}
	}
}
```

```go
func TestRenderHTMLDLQTopic(t *testing.T) {
	match, _ := RenderHTML(dlqTopicReport(true))
	if !strings.Contains(match, `class="metric-ok"`) || !strings.Contains(match, "Статус: совпадает") {
		t.Fatalf("match: %s", match)
	}
	mism, _ := RenderHTML(dlqTopicReport(false))
	if !strings.Contains(mism, "Статус: РАСХОЖДЕНИЕ") ||
		!strings.Contains(mism, `class="metric-bad"`) ||
		!strings.Contains(mism, `class="mismatch"`) {
		t.Fatalf("mismatch: %s", mism)
	}
}

func TestRenderHTMLTimeline(t *testing.T) {
	r := sampleReportFull()
	r.Timeline = []TimelineBucket{{BucketS: 100, Count: 3}, {BucketS: 200, Count: 5}}
	html, _ := RenderHTML(r)
	if !strings.Contains(html, "<h2>Таймлайн</h2>") ||
		!strings.Contains(html, `class="tl-bar"`) ||
		!strings.Contains(html, "разрыв 99с") {
		t.Fatalf("timeline: %s", html)
	}
	if !strings.Contains(html, `style="width: 60%"`) || !strings.Contains(html, `style="width: 100%"`) {
		t.Fatalf("bar widths (3/5=60%, 5/5=100%): %s", html)
	}
}
```

- [ ] **Step 2: Запустить — убедиться, что тесты падают**

Run: `go test ./internal/audit/ -run 'TestRenderHTML' -count=1`
Expected: FAIL — текущий HTML англ., без маркеров.

- [ ] **Step 3: Реализовать `RenderHTML` заново**

Заменить `<style>`-блок и тело `RenderHTML`:

```go
const htmlStyle = `
body{font-family:system-ui,sans-serif;max-width:900px;margin:24px auto;padding:0 16px;color:#222}
table{border-collapse:collapse;margin:12px 0}
th,td{border:1px solid #ccc;padding:4px 10px;text-align:left}
td.num,th.num{text-align:right}
.verdict-ok{background:#e6f4ea;color:#0a7d2e;padding:10px;border-radius:6px;font-weight:600}
.verdict-bad{background:#fdecea;color:#c0392b;padding:10px;border-radius:6px;font-weight:600}
.metric-ok{color:#0a7d2e;font-weight:600}
.metric-warn{color:#a06b00;font-weight:600}
.metric-bad{color:#c0392b;font-weight:600}
.mismatch{color:#c0392b;font-weight:600}
.tl-row{display:flex;align-items:center;gap:8px;margin:2px 0;font-size:13px}
.tl-bar-track{flex:0 0 40%;background:#f0f0f0;border-radius:3px}
.tl-bar{height:10px;background:#4a7ebb;border-radius:3px}
.tl-gap{color:#666;font-size:12px;margin:6px 0}
`

// RenderHTML — самодостаточный HTML-отчёт с цветовыми маркерами (spec §4, §6).
func RenderHTML(r Report) (string, error) {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>Отчёт о качестве данных</title><style>")
	b.WriteString(htmlStyle)
	b.WriteString("</style></head><body>\n")
	b.WriteString("<h1>Отчёт о качестве данных (audit)</h1>\n")

	healthy, problems := Verdict(r)
	if healthy {
		b.WriteString("<p class=\"verdict-ok\">Вердикт: проблем не обнаружено.</p>\n<p>")
		if r.DLQTopic != nil {
			b.WriteString("Все заложенные дефекты найдены; сверка DLQ — совпадает.</p>\n")
		} else {
			b.WriteString("Все заложенные дефекты найдены.</p>\n")
		}
	} else {
		fmt.Fprintf(&b, "<p class=\"verdict-bad\">Вердикт: обнаружены проблемы (%d).</p>\n<ul>\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(&b, "<li>%s</li>\n", p)
		}
		b.WriteString("</ul>\n")
	}

	b.WriteString("<h2>Что проверяли</h2>\n<p>")
	fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Инспектор нашёл %d записей: %d — реальные дефекты, %d — ложные срабатывания.</p>\n",
		r.LedgerEntries, r.Overall.TotalDefects, r.Overall.Findings, r.Overall.Caught, r.Overall.FalsePos)

	b.WriteString("<h2>Метрики простыми словами</h2>\n<table>\n")
	b.WriteString("<tr><th>Метрика</th><th>Значение</th><th>Что это значит</th></tr>\n")
	fmt.Fprintf(&b, "<tr><td>Recall</td><td>%d/%d (%s)</td><td>Доля заложенных дефектов, которые удалось найти</td></tr>\n",
		r.Overall.Caught, r.Overall.TotalDefects, Pct(r.Overall.Recall))
	fmt.Fprintf(&b, "<tr><td>Precision</td><td>%d/%d (%s)</td><td>Доля находок, которые подтвердились; остальные — ложные срабатывания</td></tr>\n",
		r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings, Pct(r.Overall.Precision))
	b.WriteString("</table>\n")

	b.WriteString("<h2>Дефекты по видам</h2>\n<table>\n")
	b.WriteString("<tr><th>Дефект</th><th>Что это</th><th class=\"num\">Заложено</th><th class=\"num\">Найдено</th><th class=\"num\">Recall</th><th class=\"num\">Записей</th><th class=\"num\">Ложных</th><th class=\"num\">Precision</th></tr>\n")
	for _, tm := range r.PerTag {
		recall, prec := "—", "—"
		recallCls, precCls := "", ""
		if tm.Total > 0 {
			recall = Pct(tm.Recall)
			if tm.Caught == tm.Total {
				recallCls = ` class="metric-ok"`
			} else {
				recallCls = ` class="metric-bad"`
			}
		}
		if tm.Findings > 0 {
			prec = Pct(tm.Precision)
			if tm.Precision < 1.0 {
				precCls = ` class="metric-warn"`
			}
		}
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num%s\">%s</td><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num%s\">%s</td></tr>\n",
			tm.Tag, TagDescription(tm.Tag), tm.Total, tm.Caught, recallCls, recall, tm.Findings, tm.FalsePos, precCls, prec)
	}
	b.WriteString("</table>\n")
	for _, tm := range r.PerTag {
		if (tm.Tag == "ooo" || tm.Tag == "lag") && tm.FalsePos > 0 {
			b.WriteString("<p>«Старое» событие (лаг) выглядит и как «не по порядку» — одна причина, две записи; это ожидаемое поведение демо, а не ошибка.</p>\n")
			break
		}
	}

	b.WriteString("<h2>DLQ — очередь проблемных сообщений</h2>\n<p>")
	fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, offline)<br>\n", r.DLQ.Count)
	if r.DLQTopic != nil {
		fmt.Fprintf(&b, "Фактически: %d (прочитано из брокера)<br>\n", r.DLQTopic.Count)
		if m := CheckDLQTopic(r.DLQ, *r.DLQTopic); m == "" {
			b.WriteString("<span class=\"metric-ok\">Статус: совпадает</span>")
		} else {
			fmt.Fprintf(&b, "<span class=\"metric-bad mismatch\">Статус: РАСХОЖДЕНИЕ — %s</span>", strings.TrimPrefix(m, "DLQ-сверка: "))
		}
	} else {
		b.WriteString("Фактически: не считалось (требуется запущенный брокер и флаг <code>-dlq-topic</code>)")
	}
	b.WriteString("</p>\n")

	if len(r.Timeline) > 0 {
		b.WriteString("<h2>Таймлайн</h2>\n")
		clusters := TimelineClusters(r.Timeline)
		maxSum := 0
		for _, c := range clusters {
			if c.Total > maxSum {
				maxSum = c.Total
			}
		}
		for i, c := range clusters {
			if i > 0 {
				fmt.Fprintf(&b, "<p class=\"tl-gap\">разрыв %ds — как правило, дефект «лаг»: события со «старой» отметкой времени</p>\n", c.StartS-clusters[i-1].EndS)
			}
			width := 100 * c.Total / maxSum
			if width < 1 {
				width = 1
			}
			fmt.Fprintf(&b, "<p class=\"tl-row\"><span>%s–%s (время события)</span><span class=\"tl-bar-track\"><span class=\"tl-bar\" style=\"width: %d%%\"></span></span><span>%d</span></p>\n",
				utcHMS(c.StartS), utcHMS(c.EndS), width, c.Total)
		}
	}

	b.WriteString("<h2>Как проверять отчёт за 10 секунд</h2>\n<ol>\n")
	b.WriteString("<li>Recall = 100% по всем видам? — значит, ни один заложенный дефект не просочился.</li>\n")
	b.WriteString("<li>DLQ: «совпадает»? — значит, в очереди проблемных сообщений ничего не потеряно.</li>\n")
	b.WriteString("<li>Precision ниже 100% у ooo/lag — это ожидаемо (двойные срабатывания); у остальных видов — 100%.</li>\n")
	b.WriteString("<li>Вердикт в шапке сводит всё в одну строку.</li>\n</ol>\n")

	fmt.Fprintf(&b, "<hr><p><small>Сгенерировано: %s · Данные: %s, %s<br>Машиночитаемая версия: <code>audit-report.json</code>; формат для специалистов: <code>-format text</code></small></p>\n",
		r.GeneratedAt.UTC().Format("2006-01-02 15:04 (UTC)"), r.Inputs.Ledger, r.Inputs.Findings)
	b.WriteString("</body></html>\n")
	return b.String(), nil
}
```

Примечание: `width` в HTML-тесте — целочисленное деление `100*total/maxTotal` (3/5 → 60, 5/5 → 100), min 1; соответствует MD-бара пропорционально.

- [ ] **Step 4: Запустить — HTML-тесты зелёные + весь пакет**

Run: `go test ./internal/audit/ -count=1`
Expected: PASS. `go vet ./...` — clean.

- [ ] **Step 5: Коммит**

```bash
git add internal/audit/render.go internal/audit/render_test.go
git commit -m "feat(audit): RenderHTML — human-readable ru report with color markers"
```

---

### Task 4: Доки + E2E

**Files:**
- Modify: `README.md` (секция «Аудит (audit)» — буллит «Форматы отчёта»)
- Modify: `manual_docs/reference/audit.md` (секция «Форматы отчёта» — строки MD/HTML)

**Interfaces:**
- Consumes: поведение из Task 2/3.
- Produces: синхронные с кодом пользовательские доки.

- [ ] **Step 1: README.md**

В секции «Аудит (audit)» заменить буллит
`- Форматы отчёта: text (stdout) / JSON (`-out`) / Markdown / HTML` / `  (`-from … -format md|html`).`
на:

```
  - Форматы отчёта: `text` (stdout, для специалистов, английский) и JSON (`-out`)
    — машиночитаемый; `md`/`html` (`-from … -format md|html`) — русские
    «человеческие» отчёты: вердикт в шапке, метрики простыми словами,
    легенда дефектов, таймлайн-«гистограмма», чеклист самопроверки;
    в HTML — цветовые маркеры.
```

(сохранить переносы/отступ как в surrounding-буллитах).

- [ ] **Step 2: manual_docs/reference/audit.md**

В секции «Форматы отчёта» заменить описания Markdown и HTML:

```
- **Markdown** — `-from … -format md`: русский «человеческий» отчёт —
  вердикт, «что проверяли», метрики простыми словами, дефекты с легендой,
  DLQ-сверка, таймлайн кластерами, чеклист «10 секунд», подвал.
- **HTML** — `-from … -format html`: самодостаточный файл (инлайн CSS, без JS),
  тот же контент; цветовые маркеры: `metric-ok/warn/bad`, `verdict-ok/bad`,
  расхождение DLQ — `metric-bad mismatch`.
```

- [ ] **Step 3: Полная проверка**

Run: `gofmt -l cmd internal; go build ./... && go vet ./... && go test ./... -count=1`
Expected: gofmt — пусто; build/vet OK; все пакеты PASS.

- [ ] **Step 4: E2E `make demo`**

Run: `make demo` (брокер поднимется и упадёт сам, ~60с)
Expected: exit 0; в конце — рендер `out/audit-report.md`. Проверить файл:
`grep -c "Вердикт:" out/audit-report.md` → 1; содержит `## Дефекты по видам`, `## DLQ — очередь проблемных сообщений`, `разрыв` (2 кластера), `Статус: совпадает`. Сгенерировать HTML: `./bin/audit -from out/audit-report.json -format html -out /tmp/audit.html` и открыть глазами (или grep `class="tl-bar"`). Корень репо — чистый (`git status`).

- [ ] **Step 5: Коммит**

```bash
git add README.md manual_docs/reference/audit.md
git commit -m "docs: human-readable audit report (README, manual_docs)"
```

---

## Spec-follow-up (не блокирует, после merge)

- Опциональная полировка формулировки вердикт-проблемы: «найдено N из M» вместо «найдено/всего = N/M» (round-4 Minor 3) — только при следующем изменении `explain.go`.
- Pre-existing (не эта фича): HTML-экранирование, `make -n demo` не dry-run.
