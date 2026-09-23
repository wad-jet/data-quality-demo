package producer

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func fixedNow() func() time.Time {
	t := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func TestNewGeneratorInvalidRates(t *testing.T) {
	rates := Rates{Missing: 0.6, Dup: 0.5} // sum > 1
	_, err := NewGenerator(1, rates, fixedNow())
	if err == nil {
		t.Fatalf("expected error for rates sum > 1")
	}
}

func TestGeneratorDeterminism(t *testing.T) {
	rates := Rates{}
	now := fixedNow()
	g1, err := NewGenerator(42, rates, now)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	g2, err := NewGenerator(42, rates, now)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	for i := 0; i < 20; i++ {
		e1 := g1.Next()
		e2 := g2.Next()
		if e1.Defect != e2.Defect {
			t.Fatalf("defect mismatch at %d: %v vs %v", i, e1.Defect, e2.Defect)
		}
		if string(e1.Payload) != string(e2.Payload) {
			t.Fatalf("payload mismatch at %d", i)
		}
		if !e1.Ts.Equal(e2.Ts) {
			t.Fatalf("timestamp mismatch at %d", i)
		}
	}
}

func TestGeneratorDistributionAndDup(t *testing.T) {
	rates := Rates{Missing: 0.1, Dup: 0.1, TypeDrift: 0.1, OOO: 0.1, Lag: 0.1, InvalidJSON: 0.1}
	now := fixedNow()
	g, err := NewGenerator(123, rates, now)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	const total = 10000
	counts := map[Defect]int{}
	seen := map[string]Event{}
	var dupCount int
	for i := 0; i < total; i++ {
		ev := g.Next()
		counts[ev.Defect]++
		if ev.Defect == DefectDup {
			dupCount++
			orig, ok := seen[ev.OrderID]
			if !ok {
				t.Fatalf("dup event refers to unknown order id %s", ev.OrderID)
			}
			if string(ev.Payload) != string(orig.Payload) {
				t.Fatalf("dup payload differs from original for order %s", ev.OrderID)
			}
			if !ev.Ts.Equal(orig.Ts) {
				t.Fatalf("dup timestamp differs from original for order %s", ev.OrderID)
			}
		} else {
			// store only first occurrence per order id for future dup reference
			var payloadMap map[string]interface{}
			if ev.Defect != DefectInvalidJSON {
				if err := json.Unmarshal(ev.Payload, &payloadMap); err != nil {
					t.Fatalf("invalid json payload: %v", err)
				}
				if id, ok := payloadMap["order_id"].(string); ok {
					if _, exists := seen[id]; !exists {
						seen[id] = ev
					}
				}
			}
		}
		// Check lag/ooo timestamps
		switch ev.Defect {
		case DefectLag:
			if !ev.Ts.Equal(now().Add(-5 * time.Minute)) {
				t.Fatalf("lag timestamp incorrect: %v", ev.Ts)
			}
		case DefectOOO:
			if !ev.Ts.Equal(now().Add(-5 * time.Second)) {
				t.Fatalf("ooo timestamp incorrect: %v", ev.Ts)
			}
		}
	}
	// Ensure each defect appears at least once
	for _, d := range []Defect{DefectMissing, DefectDup, DefectTypeDrift, DefectOOO, DefectLag, DefectInvalidJSON} {
		if counts[d] == 0 {
			t.Fatalf("defect %s not observed", d)
		}
	}
	// Verify frequencies within tolerance
	tolerance := 0.02
	for _, d := range []Defect{DefectMissing, DefectDup, DefectTypeDrift, DefectOOO, DefectLag, DefectInvalidJSON} {
		freq := float64(counts[d]) / float64(total)
		if math.Abs(freq-0.1) > tolerance {
			t.Fatalf("defect %s frequency %f out of tolerance", d, freq)
		}
	}
	_ = dupCount
}
