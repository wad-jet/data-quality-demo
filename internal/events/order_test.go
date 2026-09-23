package events

import (
	"reflect"
	"testing"
	"time"
)

func TestDecode(t *testing.T) {
	ts := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	validTS := ts.Format(time.RFC3339)

	tests := []struct {
		name      string
		raw       string
		wantOrder *Order
		wantKind  DecodeErrKind
		wantField string
	}{
		{
			name: "valid payload",
			raw:  `{"order_id":"o-1","amount":10.5,"currency":"USD","ts":"` + validTS + `"}`,
			wantOrder: &Order{
				OrderID:  "o-1",
				Amount:   10.5,
				Currency: "USD",
				Ts:       ts,
			},
		},
		{
			name:      "missing amount",
			raw:       `{"order_id":"o-1","currency":"USD","ts":"` + validTS + `"}`,
			wantKind:  ErrFieldMissing,
			wantField: "amount",
		},
		{
			name:      "null amount",
			raw:       `{"order_id":"o-1","amount":null,"currency":"USD","ts":"` + validTS + `"}`,
			wantKind:  ErrFieldMissing,
			wantField: "amount",
		},
		{
			name:      "missing order_id",
			raw:       `{"amount":10.5,"currency":"USD","ts":"` + validTS + `"}`,
			wantKind:  ErrFieldMissing,
			wantField: "order_id",
		},
		{
			name:      "missing ts",
			raw:       `{"order_id":"o-1","amount":10.5,"currency":"USD"}`,
			wantKind:  ErrFieldMissing,
			wantField: "ts",
		},
		{
			name:      "missing currency",
			raw:       `{"order_id":"o-1","amount":10.5,"ts":"` + validTS + `"}`,
			wantKind:  ErrFieldMissing,
			wantField: "currency",
		},
		{
			name:      "amount as string",
			raw:       `{"order_id":"o-1","amount":"10.5","currency":"USD","ts":"` + validTS + `"}`,
			wantKind:  ErrTypeDrift,
			wantField: "amount",
		},
		{
			name:      "ts not RFC3339",
			raw:       `{"order_id":"o-1","amount":10.5,"currency":"USD","ts":"2026-09-23 12:00:00"}`,
			wantKind:  ErrTypeDrift,
			wantField: "ts",
		},
		{
			name:     "broken json",
			raw:      `{`,
			wantKind: ErrInvalidJSON,
		},
		{
			name:     "empty payload",
			raw:      "",
			wantKind: ErrInvalidJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order, decErr := Decode([]byte(tt.raw))

			if tt.wantOrder != nil {
				if decErr != nil {
					t.Fatalf("expected success, got DecodeError{Kind:%q Field:%q Err:%v}", decErr.Kind, decErr.Field, decErr.Err)
				}
				if !reflect.DeepEqual(order, tt.wantOrder) {
					t.Fatalf("got %+v, want %+v", order, tt.wantOrder)
				}
				return
			}

			if decErr == nil {
				t.Fatalf("expected DecodeError Kind=%q Field=%q, got success with %+v", tt.wantKind, tt.wantField, order)
			}
			if decErr.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", decErr.Kind, tt.wantKind)
			}
			if decErr.Field != tt.wantField {
				t.Errorf("Field = %q, want %q", decErr.Field, tt.wantField)
			}
			if decErr.Err == nil {
				t.Error("Err = nil, want non-nil")
			}
			if order != nil {
				t.Errorf("order = %+v, want nil on error", order)
			}
		})
	}
}
