package checks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 23, 14, 0, 0, 0, time.UTC)

const brokenJSON = `{oops`

func orderPayload(id string, ts time.Time) []byte {
	return []byte(fmt.Sprintf(`{"order_id":%q,"amount":199.5,"currency":"USD","ts":%q}`, id, ts.Format(time.RFC3339)))
}

func envWith(payload []byte, offset int64) Envelope {
	return Envelope{Value: payload, Partition: 0, Offset: offset}
}

func TestSchemaCheck(t *testing.T) {
	c := SchemaCheck{}
	st := NewState()
	cases := []struct {
		name      string
		payload   []byte
		wantCount int
		wantCheck string
		wantID    string
	}{
		{"valid", orderPayload("o-1", now), 0, "", ""},
		{"missing_order_id", []byte(`{"amount":1.5,"currency":"USD","ts":"2026-09-23T14:00:00Z"}`), 1, "field_missing", ""},
		{"missing_amount", []byte(`{"order_id":"o-2","currency":"USD","ts":"2026-09-23T14:00:00Z"}`), 1, "field_missing", "o-2"},
		{"type_drift", []byte(`{"order_id":"o-3","amount":"NaN","currency":"USD","ts":"2026-09-23T14:00:00Z"}`), 1, "type_drift", "o-3"},
		{"invalid_json", []byte(brokenJSON), 1, "invalid_json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Inspect(context.Background(), envWith(tc.payload, 7), st)
			if len(got) != tc.wantCount {
				t.Fatalf("findings = %v, want %d", got, tc.wantCount)
			}
			if tc.wantCount > 0 {
				if got[0].Check != tc.wantCheck {
					t.Errorf("check = %q, want %q", got[0].Check, tc.wantCheck)
				}
				if got[0].OrderID != tc.wantID {
					t.Errorf("order_id = %q, want %q", got[0].OrderID, tc.wantID)
				}
				if got[0].Offset != 7 {
					t.Errorf("offset = %d, want 7", got[0].Offset)
				}
				if got[0].Detail == "" {
					t.Error("detail is empty")
				}
			}
			if len(st.Seen) != 0 || !st.LastTS.IsZero() {
				t.Errorf("state was mutated: %+v", st)
			}
		})
	}
}

func TestDuplicateCheck(t *testing.T) {
	c := DuplicateCheck{}
	st := NewState()
	ctx := context.Background()

	if got := c.Inspect(ctx, envWith(orderPayload("o-1", now), 1), st); len(got) != 0 {
		t.Fatalf("first occurrence: findings = %v, want 0", got)
	}
	if len(st.Seen) != 1 {
		t.Fatalf("Seen = %v, want 1 entry", st.Seen)
	}

	if got := c.Inspect(ctx, envWith([]byte(brokenJSON), 2), st); len(got) != 0 {
		t.Fatalf("broken event: findings = %v, want 0", got)
	}
	if len(st.Seen) != 1 {
		t.Fatalf("broken event went into Seen: %v", st.Seen)
	}
	if _, ok := st.Seen["o-1"]; !ok {
		t.Fatalf("Seen lost o-1: %v", st.Seen)
	}

	got := c.Inspect(ctx, envWith(orderPayload("o-1", now), 3), st)
	if len(got) != 1 {
		t.Fatalf("duplicate: findings = %v, want 1", got)
	}
	f := got[0]
	if f.Check != "duplicate" || f.OrderID != "o-1" || f.Offset != 3 || !f.Ts.Equal(now) {
		t.Errorf("finding = %+v", f)
	}
	if !strings.Contains(f.Detail, "o-1") {
		t.Errorf("detail %q does not contain order id", f.Detail)
	}
}

func TestOutOfOrderCheck(t *testing.T) {
	c := OutOfOrderCheck{}
	st := NewState()
	ctx := context.Background()

	old := now
	peak := now.Add(10 * time.Second)

	if got := c.Inspect(ctx, envWith(orderPayload("o-1", old), 1), st); len(got) != 0 {
		t.Fatalf("first event: %v, want 0", got)
	}
	if !st.LastTS.Equal(old) {
		t.Fatalf("LastTS = %v, want %v", st.LastTS, old)
	}

	if got := c.Inspect(ctx, envWith(orderPayload("o-2", peak), 2), st); len(got) != 0 {
		t.Fatalf("increasing ts: %v, want 0", got)
	}
	if !st.LastTS.Equal(peak) {
		t.Fatalf("LastTS = %v, want %v", st.LastTS, peak)
	}

	got := c.Inspect(ctx, envWith(orderPayload("o-3", old), 3), st)
	if len(got) != 1 || got[0].Check != "out_of_order" {
		t.Fatalf("old event: %v, want 1 out_of_order", got)
	}
	if !st.LastTS.Equal(peak) {
		t.Fatalf("LastTS was reset by old event: %v", st.LastTS)
	}

	mid := now.Add(8 * time.Second)
	got = c.Inspect(ctx, envWith(orderPayload("o-4", mid), 4), st)
	if len(got) != 1 || got[0].Check != "out_of_order" {
		t.Fatalf("ts between old and peak: %v, want 1 out_of_order (monotonic)", got)
	}

	if got := c.Inspect(ctx, envWith([]byte(brokenJSON), 5), st); len(got) != 0 {
		t.Fatalf("broken event: %v, want 0", got)
	}
	if !st.LastTS.Equal(peak) {
		t.Fatalf("LastTS changed by broken event: %v", st.LastTS)
	}
}

func TestLagCheck(t *testing.T) {
	c := NewLagCheck(60*time.Second, func() time.Time { return now })
	st := NewState()
	cases := []struct {
		name      string
		payload   []byte
		wantCount int
	}{
		{"no_lag", orderPayload("o-1", now.Add(-10*time.Second)), 0},
		{"lag", orderPayload("o-2", now.Add(-70*time.Second)), 1},
		{"broken", []byte(brokenJSON), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Inspect(context.Background(), envWith(tc.payload, 1), st)
			if len(got) != tc.wantCount {
				t.Fatalf("findings = %v, want %d", got, tc.wantCount)
			}
			if tc.wantCount > 0 && got[0].Check != "lag" {
				t.Errorf("check = %q, want lag", got[0].Check)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	reg := NewDefault(60*time.Second, func() time.Time { return now })

	var names []string
	for _, c := range reg {
		names = append(names, c.Name())
	}
	if len(names) != 4 || names[0] != "schema" || names[1] != "duplicate" || names[2] != "out_of_order" || names[3] != "lag" {
		t.Fatalf("registry order = %v", names)
	}

	st := NewState()
	ctx := context.Background()

	got := reg.Inspect(ctx, envWith([]byte(brokenJSON), 1), st)
	if len(got) != 1 || got[0].Check != "invalid_json" {
		t.Fatalf("broken event: %v, want only schema finding", got)
	}
	if len(st.Seen) != 0 || !st.LastTS.IsZero() {
		t.Fatalf("broken event changed state: %+v", st)
	}

	got = reg.Inspect(ctx, envWith(orderPayload("o-1", now), 2), st)
	if len(got) != 0 {
		t.Fatalf("valid event: %v, want 0", got)
	}
	if len(st.Seen) != 1 || !st.LastTS.Equal(now) {
		t.Fatalf("state after valid event: %+v", st)
	}

	got = reg.Inspect(ctx, envWith(orderPayload("o-1", now), 3), st)
	if len(got) != 1 || got[0].Check != "duplicate" {
		t.Fatalf("dup copy: %v, want 1 duplicate", got)
	}

	got = reg.Inspect(ctx, envWith(orderPayload("o-2", now.Add(-70*time.Second)), 4), st)
	if len(got) != 2 {
		t.Fatalf("lag event: %v, want 2 findings", got)
	}
	if got[0].Check != "out_of_order" || got[1].Check != "lag" {
		t.Fatalf("multi-trigger order: %v", got)
	}
}
