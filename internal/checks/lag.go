package checks

import (
	"context"
	"fmt"
	"time"

	"dqdemo/internal/events"
)

type LagCheck struct {
	threshold time.Duration
	now       func() time.Time
}

func NewLagCheck(threshold time.Duration, now func() time.Time) *LagCheck {
	return &LagCheck{threshold: threshold, now: now}
}

func (c *LagCheck) Name() string { return "lag" }

func (c *LagCheck) Inspect(ctx context.Context, env Envelope, st *State) []Finding {
	order, dErr := events.Decode(env.Value)
	if dErr != nil {
		return nil
	}
	lag := c.now().Sub(order.Ts)
	if lag > c.threshold {
		return []Finding{{
			Check:   "lag",
			OrderID: order.OrderID,
			Offset:  env.Offset,
			Detail:  fmt.Sprintf("event lag %s exceeds threshold %s", lag, c.threshold),
			Ts:      order.Ts,
		}}
	}
	return nil
}
