package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"dqdemo/internal/events"
)

type SchemaCheck struct{}

func (SchemaCheck) Name() string { return "schema" }

func (SchemaCheck) Inspect(ctx context.Context, env Envelope, st *State) []Finding {
	_, dErr := events.Decode(env.Value)
	if dErr == nil {
		return nil
	}
	id, ts := extractRaw(env.Value)
	if ts.IsZero() {
		ts = time.Now()
	}
	detail := dErr.Err.Error()
	if dErr.Field != "" {
		detail = fmt.Sprintf("field %s: %s", dErr.Field, dErr.Err)
	}
	return []Finding{{
		Check:   string(dErr.Kind),
		OrderID: id,
		Offset:  env.Offset,
		Detail:  detail,
		Ts:      ts,
	}}
}

type rawEvent struct {
	OrderID string    `json:"order_id"`
	Ts      time.Time `json:"ts"`
}

// extractRaw — ленивое извлечение order_id/ts из битого payload для finding;
// при ошибке возвращает нулевые значения.
func extractRaw(raw []byte) (string, time.Time) {
	var r rawEvent
	_ = json.Unmarshal(raw, &r)
	return r.OrderID, r.Ts
}
