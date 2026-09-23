package producer

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// Defect type identifier
type Defect string

const (
	DefectNone        Defect = "none"
	DefectMissing     Defect = "missing"
	DefectDup         Defect = "dup"
	DefectTypeDrift   Defect = "typedrift"
	DefectOOO         Defect = "ooo"
	DefectLag         Defect = "lag"
	DefectInvalidJSON Defect = "invalidjson"
)

type Rates struct {
	Missing     float64
	Dup         float64
	TypeDrift   float64
	OOO         float64
	Lag         float64
	InvalidJSON float64
}

func (r Rates) Sum() float64 {
	return r.Missing + r.Dup + r.TypeDrift + r.OOO + r.Lag + r.InvalidJSON
}

type Event struct {
	Payload []byte
	Defect  Defect
	OrderID string
	Ts      time.Time
}

type Generator struct {
	rand  *rand.Rand
	rates Rates
	now   func() time.Time
	seq   int64   // next order sequence number (starts at 1)
	pool  []Event // schema-valid sent events, in send order (dup source)
}

func NewGenerator(seed int64, rates Rates, now func() time.Time) (*Generator, error) {
	if rates.Sum() > 1.0 {
		return nil, errors.New("rates sum exceeds 1")
	}
	if now == nil {
		now = time.Now
	}
	return &Generator{
		rand:  rand.New(rand.NewSource(seed)),
		rates: rates,
		now:   now,
		seq:   1,
	}, nil
}

func (g *Generator) Next() Event {
	// decide if any defect occurs
	p := g.rand.Float64()
	total := g.rates.Sum()
	var defect Defect = DefectNone
	if p < total && total > 0 {
		// cumulative intervals
		cum := 0.0
		if g.rates.Missing > 0 {
			cum += g.rates.Missing
			if p < cum {
				defect = DefectMissing
			}
		}
		if defect == DefectNone && g.rates.Dup > 0 {
			cum += g.rates.Dup
			if p < cum {
				defect = DefectDup
			}
		}
		if defect == DefectNone && g.rates.TypeDrift > 0 {
			cum += g.rates.TypeDrift
			if p < cum {
				defect = DefectTypeDrift
			}
		}
		if defect == DefectNone && g.rates.OOO > 0 {
			cum += g.rates.OOO
			if p < cum {
				defect = DefectOOO
			}
		}
		if defect == DefectNone && g.rates.Lag > 0 {
			cum += g.rates.Lag
			if p < cum {
				defect = DefectLag
			}
		}
		if defect == DefectNone && g.rates.InvalidJSON > 0 {
			cum += g.rates.InvalidJSON
			if p < cum {
				defect = DefectInvalidJSON
			}
		}
	}

	// handle dup specially: re-send a copy of a schema-valid original
	if defect == DefectDup {
		if len(g.pool) == 0 {
			defect = DefectNone
		} else {
			orig := g.pool[g.rand.Intn(len(g.pool))]
			return Event{
				Payload: orig.Payload,
				Defect:  DefectDup,
				OrderID: orig.OrderID,
				Ts:      orig.Ts,
			}
		}
	}

	// generate new order id
	orderID := fmt.Sprintf("o-%06d", g.seq)
	g.seq++

	// base timestamp
	ts := g.now()
	switch defect {
	case DefectOOO:
		ts = g.now().Add(-5 * time.Second)
	case DefectLag:
		ts = g.now().Add(-5 * time.Minute)
	}

	// build payload according to defect type
	var payload []byte
	switch defect {
	case DefectMissing:
		// omit amount field, keep others
		m := map[string]interface{}{
			"order_id": orderID,
			// "amount": omitted
			"currency": "USD",
			"ts":       ts.UTC().Format(time.RFC3339),
		}
		// randomly decide to omit amount or currency? We'll omit amount.
		// Ensure JSON marshaling succeeds.
		payload, _ = json.Marshal(m)
	case DefectTypeDrift:
		// amount as string
		m := map[string]interface{}{
			"order_id": orderID,
			"amount":   fmt.Sprintf("%0.2f", 100.0), // string representation
			"currency": "USD",
			"ts":       ts.UTC().Format(time.RFC3339),
		}
		payload, _ = json.Marshal(m)
	case DefectInvalidJSON:
		payload = []byte("{ invalid json ")
	default:
		// normal payload
		m := map[string]interface{}{
			"order_id": orderID,
			"amount":   100.0,
			"currency": "USD",
			"ts":       ts.UTC().Format(time.RFC3339),
		}
		payload, _ = json.Marshal(m)
	}

	ev := Event{Payload: payload, Defect: defect, OrderID: orderID, Ts: ts}
	// only schema-valid events are eligible as dup originals
	if defect == DefectNone || defect == DefectOOO || defect == DefectLag {
		g.pool = append(g.pool, ev)
	}
	return ev
}
