package checks

// IsSchemaViolation — true для проверок, чьи findings уходят в DLQ
// (нарушения схемы). Единый источник: consumer, internal/dq, audit.
func IsSchemaViolation(check string) bool {
	switch check {
	case "field_missing", "type_drift", "invalid_json":
		return true
	default:
		return false
	}
}
