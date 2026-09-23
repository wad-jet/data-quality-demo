package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Order struct {
	OrderID  string    `json:"order_id"`
	Amount   float64   `json:"amount"`
	Currency string    `json:"currency"`
	Ts       time.Time `json:"ts"`
}

type DecodeErrKind string

const (
	ErrFieldMissing DecodeErrKind = "field_missing"
	ErrTypeDrift    DecodeErrKind = "type_drift"
	ErrInvalidJSON  DecodeErrKind = "invalid_json"
)

type DecodeError struct {
	Kind  DecodeErrKind
	Field string // поле, если применимо
	Err   error
}

// wire — промежуточный struct с pointer-полями для детекции отсутствующих и null-полей.
type wire struct {
	OrderID  *string    `json:"order_id"`
	Amount   *float64   `json:"amount"`
	Currency *string    `json:"currency"`
	Ts       *time.Time `json:"ts"`
}

// Decode — строгая декодирука: все 4 поля обязательны.
func Decode(raw []byte) (*Order, *DecodeError) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		var synErr *json.SyntaxError
		if errors.As(err, &synErr) {
			return nil, &DecodeError{Kind: ErrInvalidJSON, Err: err}
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return nil, &DecodeError{Kind: ErrTypeDrift, Field: typeErr.Field, Err: err}
		}
		// Остальные ошибки (напр. *time.ParseError) приходят только от поля ts.
		return nil, &DecodeError{Kind: ErrTypeDrift, Field: "ts", Err: err}
	}

	if w.OrderID == nil {
		return nil, missing("order_id")
	}
	if w.Amount == nil {
		return nil, missing("amount")
	}
	if w.Currency == nil {
		return nil, missing("currency")
	}
	if w.Ts == nil {
		return nil, missing("ts")
	}

	return &Order{
		OrderID:  *w.OrderID,
		Amount:   *w.Amount,
		Currency: *w.Currency,
		Ts:       *w.Ts,
	}, nil
}

func missing(field string) *DecodeError {
	return &DecodeError{
		Kind:  ErrFieldMissing,
		Field: field,
		Err:   fmt.Errorf("field %q is missing or null", field),
	}
}
