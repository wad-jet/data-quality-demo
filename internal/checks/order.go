package checks

import (
	"context"
	"fmt"
	"time"

	"dqdemo/internal/events"
)

type OutOfOrderCheck struct{}

func (OutOfOrderCheck) Name() string { return "out_of_order" }

func (OutOfOrderCheck) Inspect(ctx context.Context, env Envelope, st *State) []Finding {
	order, dErr := events.Decode(env.Value)
	if dErr != nil {
		return nil
	}
	var out []Finding
	if !st.LastTS.IsZero() && order.Ts.Before(st.LastTS) {
		out = append(out, Finding{
			Check:   "out_of_order",
			OrderID: order.OrderID,
			Offset:  env.Offset,
			Detail:  fmt.Sprintf("event ts %s before last ts %s", order.Ts.Format(time.RFC3339), st.LastTS.Format(time.RFC3339)),
			Ts:      order.Ts,
		})
	}
	if order.Ts.After(st.LastTS) {
		st.LastTS = order.Ts
	}
	return out
}
