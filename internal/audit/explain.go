package audit

import (
	"fmt"
	"math"
	"strings"
)

// TagDescriptions provides Russian descriptions for defect tags.
var TagDescriptions = map[string]string{
	"missing":     "заказ без обязательного поля (например, суммы) — корректно обработать его нельзя",
	"dup":         "повторная отправка одного заказа — без защиты от дублей заказ может быть обработан дважды",
	"typedrift":   "поле сменило тип (число стало текстом и т.п.) — потребитель, ожидающий число, упадёт или молча посчитает неверно",
	"ooo":         "события пришли не по порядку (событие с более поздней отметкой времени приходит раньше события с более ранней) — состояние заказа соберётся неверно",
	"lag":         "«старое» событие: отметка времени в прошлом — обрабатывается «задним числом» и может перезаписать уже актуальное состояние",
	"invalidjson": "некорректный JSON — сообщение не удаётся прочитать",
}

// TagDescription returns a human‑readable description for a tag.
// Unknown tags are returned unchanged.
func TagDescription(tag string) string {
	if d, ok := TagDescriptions[tag]; ok {
		return d
	}
	return tag
}

// Verdict evaluates a Report and returns overall health and a list of problem strings.
// It reports per‑tag recall problems, DLQ mismatches, and any warnings.
func Verdict(r Report) (healthy bool, problems []string) {
	problems = []string{}
	// per‑tag recall check
	for _, tm := range r.PerTag {
		if tm.Total > 0 && tm.Caught < tm.Total {
			problems = append(problems, fmt.Sprintf("не все дефекты вида %s найдены: найдено/всего = %d/%d", tm.Tag, tm.Caught, tm.Total))
		}
	}
	// DLQ topic verification
	dlqProblem := false
	if r.DLQTopic != nil {
		if m := CheckDLQTopic(r.DLQ, *r.DLQTopic); m != "" {
			dlqProblem = true
			// keep the original message (tests only look for "DLQ")
			problems = append(problems, m)
		}
	}
	// warnings – deduplicate DLQ warning if already reported as a problem
	for _, w := range r.Warnings {
		if dlqProblem && strings.HasPrefix(w, "DLQ-сверка") {
			continue
		}
		problems = append(problems, w)
	}
	return len(problems) == 0, problems
}

// TimelineCluster groups consecutive timeline buckets.
type TimelineCluster struct {
	StartS int64
	EndS   int64
	Total  int
}

// TimelineClusters splits buckets into clusters; a gap >60 seconds starts a new cluster.
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

// BarWidth returns an ASCII bar width between 1 and 40.
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

// Pct formats a fraction as a percentage with one decimal place.
func Pct(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}
