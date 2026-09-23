package checks

import (
	"context"
	"fmt"

	"dqdemo/internal/events"
)

type DuplicateCheck struct{}

func (DuplicateCheck) Name() string { return "duplicate" }

func (DuplicateCheck) Inspect(ctx context.Context, env Envelope, st *State) []Finding {
	order, dErr := events.Decode(env.Value)
	if dErr != nil {
		return nil
	}
	if _, seen := st.Seen[order.OrderID]; seen {
		return []Finding{{
			Check:   "duplicate",
			OrderID: order.OrderID,
			Offset:  env.Offset,
			Detail:  fmt.Sprintf("order %s already seen", order.OrderID),
			Ts:      order.Ts,
		}}
	}
	st.Seen[order.OrderID] = struct{}{}
	return nil
}
