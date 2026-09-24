# Informative Audit Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Сделать человекочитаемые md/html-отчёты аудита самодостаточными: понятные колонки таблицы, сходные цифры (раскладка K), пояснения секций DLQ/таймлайн, описания дефектов «что + почему плохо», корректный чеклист.

**Architecture:** Все изменения — в двух рендерах (`RenderMarkdown`/`RenderHTML` в `internal/audit/render.go`) и в легенде (`explain.go: TagDescriptions`). Новые величины (K = Σ(F−FP) − Σ(Caught), раскладка DLQ по видам) вычисляются рендером из существующих полей `Report` — JSON-схема, `text`-формат, CLI, вердикт не меняются.

**Tech Stack:** Go 1.26, stdlib only (strings.Builder, fmt).

**Spec:** `docs/superpowers/specs/2026-09-25-audit-report-informative-design.md`

## Global Constraints

- Все новые пользовательские тексты — русский; `text`/JSON — английский и не меняются (spec §3, §4).
- JSON-схема `audit-report.json`, CLI-флаги, exit codes, `Verdict`, `CheckDLQTopic`, `TimelineClusters`, `BarWidth`, `Pct` — не меняются (spec §4).
- Порядок элементов под таблицей фиксирован: таблица → пометка ooo/lag (если условие) → блок «Как читать колонки» (spec §3.1).
- Порядок слагаемых DLQ-раскладки — по `tagOrder`, lookup в `ByReason` (ключи — имена check'ов, отображение `tagToCheck`), НЕ итерация по map (spec §3.5).
- Раскладка «Что проверяли»: K <= 0 → пункта про дополнительные записи нет; Caught < TotalDefects → «(найдено X из Y)»; Findings == 0 → «Записей инспектора (consumer — читает каждое событие и фиксирует нарушения) нет.» (spec §3.2).
- Формы числительно-независимые: «— записей всего N:», «Подтвердились записи: N из M —», «Всего записей: N» (spec §3.2/§3.6, review round 3).
- HTML-экранирование — pre-existing follow-up, не делаем (spec §4).
- gofmt-чистота, `go vet ./...` clean после каждого task.

## Review Focus

1. `K <= 0` (включая синтетически отрицательный) → пункт «- K — дополнительные записи…» отсутствует, сумма остальных пунктов + K-логика не ломает текст. Test: `TestRenderMarkdownAllFound` (Task 2).
2. `Findings == 0` → строка «Записей инспектора (consumer …) нет.», без списка и без «Подтвердились…». Test: `TestRenderMarkdownEmptyReport` (Task 2).
3. Ветки caught-заметки: `Caught < TotalDefects` → «(найдено 2 из 3)» (основная фикстура), `Caught == TotalDefects > 0` → «(найдены все)». Tests: `TestRenderMarkdown`, `TestRenderMarkdownAllFound` (Task 2).
4. DLQ-раскладка: детерминированный порядок (missing, typedrift, invalidjson), только ненулевые, сумма = Count; `Count == 0` → без раскладки. Test: `TestRenderMarkdown` («Должно быть: 1 = 1 (missing) …») + E2E (Task 4: «112 = 42 (missing) + 47 (typedrift) + 23 (invalidjson)»).
5. Арифметическая сходимость «Что проверяли»: Caught + K + FalsePos == Findings и «Подтвердились записи: (Findings−FalsePos) из Findings». Test: `TestRenderMarkdown` (2 + 1 + 1 = 4; «3 из 4»).

---

### Task 1: Новые описания дефектов (explain.go)

**Files:**
- Modify: `internal/audit/explain.go` (мапа `TagDescriptions`)
- Test: `internal/audit/explain_test.go`

**Interfaces:**
- Consumes: — (существующая мапа)
- Produces: `TagDescriptions` — 6 новых текстов (используются рендерами в Task 2/3)

- [ ] **Step 1: Обновить тесты описаний (RED)**

В `internal/audit/explain_test.go` заменить/добавить:

```go
func TestTagDescription(t *testing.T) {
	cases := map[string]string{
		"missing":     "заказ без обязательного поля (например, суммы) — корректно обработать его нельзя",
		"dup":         "повторная отправка одного заказа — без защиты от дублей заказ может быть обработан дважды",
		"typedrift":   "поле сменило тип (число стало текстом и т.п.) — потребитель, ожидающий число, упадёт или молча посчитает неверно",
		"ooo":         "события пришли не по порядку (событие с более поздней отметкой времени приходит раньше события с более ранней) — состояние заказа соберётся неверно",
		"lag":         "«старое» событие: отметка времени в прошлом — обрабатывается «задним числом» и может перезаписать уже актуальное состояние",
		"invalidjson": "некорректный JSON — сообщение не удаётся прочитать",
	}
	for tag, want := range cases {
		if got := TagDescription(tag); got != want {
			t.Errorf("TagDescription(%q) = %q, want %q", tag, got, want)
		}
	}
	if got := TagDescription("unknown_tag"); got != "unknown_tag" {
		t.Errorf("unknown tag passthrough = %q", got)
	}
}
```

(Если существующий `TestTagDescription` слабее — заменить целиком.)

- [ ] **Step 2: RED-проверка**

Run: `go test ./internal/audit/ -run TestTagDescription -count=1`
Expected: FAIL (старые тексты).

- [ ] **Step 3: Замена мапы**

В `internal/audit/explain.go`:

```go
var TagDescriptions = map[string]string{
	"missing":     "заказ без обязательного поля (например, суммы) — корректно обработать его нельзя",
	"dup":         "повторная отправка одного заказа — без защиты от дублей заказ может быть обработан дважды",
	"typedrift":   "поле сменило тип (число стало текстом и т.п.) — потребитель, ожидающий число, упадёт или молча посчитает неверно",
	"ooo":         "события пришли не по порядку (событие с более поздней отметкой времени приходит раньше события с более ранней) — состояние заказа соберётся неверно",
	"lag":         "«старое» событие: отметка времени в прошлом — обрабатывается «задним числом» и может перезаписать уже актуальное состояние",
	"invalidjson": "некорректный JSON — сообщение не удаётся прочитать",
}
```

- [ ] **Step 4: GREEN + ripple**

Run: `go test ./internal/audit/ -count=1`
Expected: PASS. (Ripple: строка missing в `TestRenderMarkdownEmptyReport` и `TestRenderMarkdown` (Task 2 её обновит; если md-тесты упали здесь — это ожидаемый ripple, дособерётся в Task 2, но пакет должен собираться.) Для чистоты: в этом же коммите привести упавшие ассерты в `render_test.go` к новым описаниям (точечные замены строки missing/dup/typedrift/ooo/lag).)

- [ ] **Step 5: Commit**

```bash
git add internal/audit/explain.go internal/audit/explain_test.go internal/audit/render_test.go
git commit -m "feat(audit): defect descriptions — what + why it matters (ru)"
```

---

### Task 2: RenderMarkdown — колонки, легенда, раскладка, секции

**Files:**
- Modify: `internal/audit/render.go:47-143` (RenderMarkdown)
- Modify: `internal/audit/render_test.go` (фикстура + md-тесты)
- Test: `internal/audit/render_test.go`

**Interfaces:**
- Consumes: `TagDescriptions` (Task 1), `Verdict`, `Pct`, `BarWidth`, `TimelineClusters`, `CheckDLQTopic`, `tagOrder`, `tagToCheck` (audit.go, тот же пакет), `checks.IsSchemaViolation`
- Produces: `RenderMarkdown(r Report) (string, error)` — новые тексты (Task 3 зеркалит их в HTML)

- [ ] **Step 1: Обновить фикстуру (RED-подготовка)**

В `render_test.go`, `sampleReportFull()`: dup-тег `Findings: 3, FalsePos: 1, Precision: 2.0/3` (т.е. `Precision: 0.6667`), `Overall{TotalDefects: 3, Caught: 2, Recall: 0.6667, Findings: 4, FalsePos: 1, Precision: 0.75}`, `Timeline: []TimelineBucket{{BucketS: 1758621600, Count: 2}, {BucketS: 1758621700, Count: 2}}` (Σ=4, разрыв 100с). Остальное без изменений.

- [ ] **Step 2: Заменить md-тесты (RED)**

Заменить `TestRenderMarkdown`, `TestRenderMarkdownDLQTopic`, `TestRenderMarkdownTimeline`, `TestRenderMarkdownEmptyReport`, `TestRenderMarkdownNoOooNoteForOtherTags` и добавить `TestRenderMarkdownAllFound`:

```go
func TestRenderMarkdown(t *testing.T) {
	md, err := RenderMarkdown(sampleReportFull())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, m := range []string{
		"# Отчёт о качеству данных (audit)",
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

func TestRenderMarkdownDLQTopic(t *testing.T) {
	match, err := RenderMarkdown(dlqTopicReport(true))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(match, "Статус: совпадает") {
		t.Fatalf("match: %s", match)
	}
	if !strings.Contains(match, "В DLQ попадают только нарушения схемы") {
		t.Fatalf("dlq sentence: %s", match)
	}
	mism, err := RenderMarkdown(dlqTopicReport(false))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(mism, "Статус: РАСХОЖДЕНИЕ") {
		t.Fatalf("mismatch: %s", mism)
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
```

Примечания:
- `dlqTopicReport(match bool)` — существующий хелпер, не менять (он строит отчёт с DLQTopic; если в нём свои PerTag/Overall — сохранить как есть, ассерты не зависят от чисел).
- Разрывы: `TimelineClusters` считает `EndS` = последний `BucketS` кластера (без +1), разрыв = `c.StartS − prev.EndS`. `TestRenderMarkdownTimeline`: кластеры [100,101] и [200] → 200−101 = **99с**. Фикстура main: два бакета 1758621600/1758621700 (разница 100с > порога 60с) → два кластера, «разрыв 100с».

- [ ] **Step 3: RED-проверка**

Run: `go test ./internal/audit/ -run 'TestRenderMarkdown' -count=1`
Expected: FAIL (старые тексты/имена колонок).

- [ ] **Step 4: Реализация RenderMarkdown**

В `internal/audit/render.go` заменить тело `RenderMarkdown` (секции; header/verdict/metrics/footer сохраняются):

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
	k := r.Overall.Findings - r.Overall.FalsePos - r.Overall.Caught
	if r.Overall.Findings == 0 {
		fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Записей инспектора (consumer — читает каждое событие и фиксирует нарушения) нет.\n\n",
			r.LedgerEntries, r.Overall.TotalDefects)
	} else {
		caughtNote := "(найдены все)"
		if r.Overall.Caught < r.Overall.TotalDefects {
			caughtNote = fmt.Sprintf("(найдено %d из %d)", r.Overall.Caught, r.Overall.TotalDefects)
		}
		fmt.Fprintf(&b, "Producer отправил %d событий, из них с заложенными дефектами: %d. Инспектор (consumer — читает каждое событие и фиксирует нарушения) — записей всего %d:\n",
			r.LedgerEntries, r.Overall.TotalDefects, r.Overall.Findings)
		fmt.Fprintf(&b, "- %d — заложенные дефекты %s,\n", r.Overall.Caught, caughtNote)
		if k > 0 {
			fmt.Fprintf(&b, "- %d — дополнительные записи на те же дефекты (повторные отправки),\n", k)
		}
		fmt.Fprintf(&b, "- %d — ложные срабатывания (не подтвердились при сверке с ledger).\n", r.Overall.FalsePos)
		fmt.Fprintf(&b, "\nПодтвердились записи: %d из %d — это и есть Precision.\n\n",
			r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings)
	}

	b.WriteString("## Метрики простыми словами\n\n")
	b.WriteString("| Метрика | Значение | Что это значит |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Recall | %d/%d (%s) | Доля заложенных дефектов, которые удалось найти |\n",
		r.Overall.Caught, r.Overall.TotalDefects, Pct(r.Overall.Recall))
	fmt.Fprintf(&b, "| Precision | %d/%d (%s) | Доля находок, которые подтвердились; остальные — ложные срабатывания |\n",
		r.Overall.Findings-r.Overall.FalsePos, r.Overall.Findings, Pct(r.Overall.Precision))
	b.WriteString("\n")

	b.WriteString("## Дефекты по видам\n\n")
	b.WriteString("| Дефект | Что это | Заложено | Найдено из заложенных | Recall | Всего записей | Ложных | Precision |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
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
			b.WriteString("Событие с «старой» отметкой времени (lag) приходит и не по порядку — поэтому инспектор фиксирует его дважды: как lag и как ooo. Запись ooo не совпадает ни с одним заложенным ooo-дефектом и считается для ooo «ложной», хотя порядок действительно был нарушен. Это ожидаемое поведение демо, а не ошибка детектора.\n\n")
			break
		}
	}
	b.WriteString("**Как читать колонки:**\n\n")
	b.WriteString("- **Заложено** — сколько дефектов этого вида producer вживил намеренно (записи ledger).\n")
	b.WriteString("- **Найдено из заложенных** — сколько из них инспектор нашёл (Заложено = Найдено → Recall 100%).\n")
	b.WriteString("- **Всего записей** — все записи инспектора этого вида. Запись — на сообщение, а «заложено/найдено» — на дефект: один дефект может дать несколько записей (повторная отправка сообщения).\n")
	b.WriteString("- **Ложных** — записи, не подтвердившиеся при сверке с ledger.\n\n")

	b.WriteString("## DLQ (dead-letter queue) — очередь проблемных сообщений\n\n")
	if r.DLQ.Count > 0 {
		var parts []string
		for _, tag := range tagOrder {
			check := tagToCheck[tag]
			if checks.IsSchemaViolation(check) {
				if n := r.DLQ.ByReason[check]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d (%s)", n, tag))
				}
			}
		}
		fmt.Fprintf(&b, "Должно быть: %d = %s (по находкам инспектора, без обращения к брокеру)\n",
			r.DLQ.Count, strings.Join(parts, " + "))
	} else {
		fmt.Fprintf(&b, "Должно быть: %d (по находкам инспектора, без обращения к брокеру)\n", r.DLQ.Count)
	}
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
	b.WriteString("В DLQ попадают только нарушения схемы (missing, typedrift, invalidjson); dup, ooo и lag — валидные сообщения, остаются в основном топике и фиксируются только записями инспектора.\n\n")

	if len(r.Timeline) > 0 {
		b.WriteString("## Таймлайн\n\n")
		total := 0
		for _, bk := range r.Timeline {
			total += bk.Count
		}
		fmt.Fprintf(&b, "Распределение записей инспектора по **времени события** — отметке в самом событии, а не моменту получения (для некорректного JSON отметка неизвестна — берётся момент получения). Всего записей: %d — это все записи из раздела «Что проверяли».\n\n", total)
		clusters := TimelineClusters(r.Timeline)
		maxSum := 0
		for _, c := range clusters {
			if c.Total > maxSum {
				maxSum = c.Total
			}
		}
		for i, c := range clusters {
			if i > 0 {
				fmt.Fprintf(&b, "разрыв %dс — событий с таким временем события не было: так проявляется дефект «лаг» — события отправляются сейчас, но несут «прошлую» отметку времени, и между ними и свежими событиями образуется разрыв\n\n", c.StartS-clusters[i-1].EndS)
			}
			fmt.Fprintf(&b, "%s–%s (время события) %s %d\n",
				utcHMS(c.StartS), utcHMS(c.EndS), strings.Repeat("█", BarWidth(c.Total, maxSum)), c.Total)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Как проверять отчёт за 10 секунд\n\n")
	b.WriteString("1. Recall = 100% по всем видам? — значит, ни один заложенный дефект не просочился.\n")
	b.WriteString("2. DLQ: «совпадает»? — значит, в очереди проблемных сообщений ничего не потеряно.\n")
	b.WriteString("3. Precision = 100% у всех видов, кроме ooo (и иногда lag): у них ниже 100% — ожидаемо: «старое» событие фиксируется и как lag, и как ooo, и запись ooo не совпадает с заложенными ooo-дефектами (см. пометку после таблицы). Ниже 100% у любого другого вида — повод разбираться.\n")
	b.WriteString("4. Сошлись пункты 1–3 и в отчёте нет предупреждений (warnings о неизвестных проверках/дефектах) — в шапке будет «проблем не обнаружено»; иначе шапка назовёт причину.\n\n")

	fmt.Fprintf(&b, "---\n*Сгенерировано: %s · Данные: %s, %s*\n",
		r.GeneratedAt.UTC().Format("2006-01-02 15:04 (UTC)"), r.Inputs.Ledger, r.Inputs.Findings)
	b.WriteString("*Машиночитаемая версия: `audit-report.json`; формат для специалистов: `-format text`*\n")
	return b.String(), nil
}
```

Добавить импорт `dqdemo/internal/checks` в render.go (нет цикла: checks → audit не импортирует).

- [ ] **Step 5: GREEN**

Run: `go test ./internal/audit/ ./cmd/audit/ -count=1`
Expected: PASS. `gofmt -l internal/audit cmd/audit` — пусто. `go vet ./...` — clean.

- [ ] **Step 6: Commit**

```bash
git add internal/audit/render.go internal/audit/render_test.go
git commit -m "feat(audit): RenderMarkdown — column semantics, K breakdown, section explanations"
```

---

### Task 3: RenderHTML — зеркало Task 2

**Files:**
- Modify: `internal/audit/render.go` (RenderHTML)
- Test: `internal/audit/render_test.go` (html-тесты)

**Interfaces:**
- Consumes: то же, что Task 2
- Produces: `RenderHTML(r Report) (string, error)` — те же тексты, что md, в HTML-разметке

- [ ] **Step 1: Заменить html-тесты (RED)**

`TestRenderHTML`, `TestRenderHTMLDLQTopic`, `TestRenderHTMLTimeline`, `TestRenderHTMLZeroCountTimeline` — расширить/заменить ассерты (тексты = как в Task 2, Step 2):

```go
func TestRenderHTML(t *testing.T) {
	html, err := RenderHTML(sampleReportFull())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, m := range []string{
		`<h1>Отчёт о качестве данных (audit)</h1>`,
		`class="verdict-bad"`,
		"<h2>Что проверяли</h2>",
		"Инспектор (consumer — читает каждое событие и фиксирует нарушения) — записей всего 4:",
		"<li>2 — заложенные дефекты (найдено 2 из 3),</li>",
		"<li>1 — дополнительные записи на те же дефекты (повторные отправки),</li>",
		"<li>1 — ложные срабатывания (не подтвердились при сверке с ledger).</li>",
		"Подтвердились записи: 3 из 4 — это и есть Precision.",
		"<h2>Дефекты по видам</h2>",
		"<th class=\"num\">Найдено из заложенных</th>",
		"<th class=\"num\">Всего записей</th>",
		"заказ без обязательного поля (например, суммы) — корректно обработать его нельзя",
		`class="num metric-bad"`,
		`class="num metric-ok"`,
		`class="num metric-warn"`,
		"<p>Событие с «старой» отметкой времени (lag) приходит и не по порядку",
		"<p><strong>Как читать колонки:</strong></p>",
		"<li><strong>Заложено</strong> — сколько дефектов этого вида producer вживил намеренно (записи ledger).</li>",
		"<h2>DLQ (dead-letter queue) — очередь проблемных сообщений</h2>",
		"Должно быть: 1 = 1 (missing) (по находкам инспектора, без обращения к брокеру)",
		"Фактически: не считалось",
		"В DLQ попадают только нарушения схемы (missing, typedrift, invalidjson)",
		"<h2>Таймлайн</h2>",
		"а не моменту получения",
		"Всего записей: 4 — это все записи из раздела «Что проверяли».",
		"Как проверять отчёт за 10 секунд",
		"кроме ooo (и иногда lag)",
		"и в отчёте нет предупреждений",
	} {
		if !strings.Contains(html, m) {
			t.Fatalf("HTML missing %q", m)
		}
	}
}
```

`TestRenderHTMLDLQTopic` / `TestRenderHTMLTimeline` / `TestRenderHTMLZeroCountTimeline` — сохранить существующие ассерты + добавить: `Статус: совпадает`/`Статус: РАСХОЖДЕНИЕ` + `class="metric-bad mismatch"` (как сейчас), «В DLQ попадают только нарушения схемы» (DLQTopic-тест), «Всего записей: 10 —» и «разрыв … событий с таким временем события не было» (timeline-тест; число разрыва — по фактическому EndS из explain.go, как в Task 2).

- [ ] **Step 2: RED-проверка**

Run: `go test ./internal/audit/ -run 'TestRenderHTML' -count=1`
Expected: FAIL.

- [ ] **Step 3: Реализация**

В `RenderHTML` внести те же текстовые изменения, что в Task 2 (зеркало 1:1):
- «Что проверяли»: `<p>Producer отправил … — записей всего N:</p><ul><li>…</li><li>…</li><li>…</li></ul><p>Подтвердились записи: N из M — это и есть Precision.</p>`; ветка `Findings == 0` → `<p>… Записей инспектора (consumer — читает каждое событие и фиксирует нарушения) нет.</p>`.
- Таблица: `<th>` «Найдено из заложенных», «Всего записей» (классы num сохраняются).
- Пометка ooo/lag: новый текст в `<p>`, сразу после `</table>`.
- Легенда: `<p><strong>Как читать колонки:</strong></p><ul><li><strong>Заложено</strong> — …</li>…</ul>` (4 пункта, тексты как в md).
- DLQ: `<h2>DLQ (dead-letter queue) — очередь проблемных сообщений</h2>`, строка «Должно быть» с раскладкой (та же логика tagOrder/ByReason/IsSchemaViolation, что в md), предложение «В DLQ попадают только нарушения схемы…» — последним в секции (после статуса/«Фактически»), в том же `<p>`.
- Таймлайн: `<p>`-вводная («Распределение записей инспектора по времени события — отметке в самом событии, а не моменту получения (для некорректного JSON отметка неизвестна — берётся момент получения). Всего записей: N — это все записи из раздела «Что проверяли».»), текст разрыва в `<p class="tl-gap">` — как в md.
- Чеклист: пункты 3 и 4 — новые тексты (как в md).

- [ ] **Step 4: GREEN**

Run: `go test ./internal/audit/ ./cmd/audit/ -count=1` → PASS. `gofmt -l internal/audit cmd/audit` → пусто. `go vet ./...` → clean.

- [ ] **Step 5: Commit**

```bash
git add internal/audit/render.go internal/audit/render_test.go
git commit -m "feat(audit): RenderHTML — same informative texts and structure"
```

---

### Task 4: Доки + E2E

**Files:**
- Modify: `README.md` (секция «Аудит»), `manual_docs/reference/audit.md` (секция «Форматы отчёта»)
- Test: E2E `make demo`

**Interfaces:**
- Consumes: финальные тексты рендеров (Tasks 2–3)

- [ ] **Step 1: README.md**

В буллете «Форматы отчёта» (сейчас перечисляет секции) заменить перечисление на:
«вердикт, «что проверяли» (раскладка: заложенные / дополнительные записи / ложные), метрики простыми словами, таблица дефектов по видам с легендой «как читать колонки», DLQ (раскладка по видам), таймлайн с барами, «как проверить отчёт за 10 секунд»».

- [ ] **Step 2: manual_docs/reference/audit.md**

В буллетах Markdown/HTML секции «Форматы отчёта» обновить перечисление секций/колонок под новые тексты (имена колонок «Найдено из заложенных», «Всего записей»; блок «Как читать колонки»; раскладка DLQ по видам; вводная таймлайна).

- [ ] **Step 3: E2E**

Run: `make demo` (полный прогон, ~1–2 мин).
Затем:
```bash
grep -q "Найдено из заложенных" out/audit-report.md
grep -q "Как читать колонки" out/audit-report.md
grep -q "дополнительные записи" out/audit-report.md
grep -q "dead-letter queue" out/audit-report.md
grep -q "не моменту получения" out/audit-report.md
grep -q "кроме ooo" out/audit-report.md
```
Все найдены. Дополнительно (визуально, `head -40 out/audit-report.md`): раскладка DLQ «112 = 42 (missing) + 47 (typedrift) + 23 (invalidjson)», сумма пунктов «Что проверяли» = числу записей.

- [ ] **Step 4: Финальные проверки**

`gofmt -l .` → пусто; `go build ./...`; `go vet ./...`; `go test ./... -count=1` → все ok.

- [ ] **Step 5: Commit**

```bash
git add README.md manual_docs/reference/audit.md
git commit -m "docs: informative audit report — column semantics and section explanations"
```
