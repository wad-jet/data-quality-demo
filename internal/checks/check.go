package checks

import (
	"context"
	"time"
)

type Envelope struct {
	Key       []byte
	Value     []byte
	Partition int32
	Offset    int64
}

type Finding struct {
	Check   string    `json:"check"`
	OrderID string    `json:"order_id,omitempty"`
	Offset  int64     `json:"offset"`
	Detail  string    `json:"detail"`
	Ts      time.Time `json:"ts"`
}

type State struct {
	Seen   map[string]struct{}
	LastTS time.Time
}

func NewState() *State {
	return &State{Seen: make(map[string]struct{})}
}

type Check interface {
	Name() string
	Inspect(ctx context.Context, env Envelope, st *State) []Finding
}

type Registry []Check

func (r Registry) Inspect(ctx context.Context, env Envelope, st *State) []Finding {
	var out []Finding
	for _, c := range r {
		out = append(out, c.Inspect(ctx, env, st)...)
	}
	return out
}

func NewDefault(lagThreshold time.Duration, now func() time.Time) Registry {
	return Registry{
		SchemaCheck{},
		DuplicateCheck{},
		OutOfOrderCheck{},
		NewLagCheck(lagThreshold, now),
	}
}
