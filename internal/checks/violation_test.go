package checks

import "testing"

func TestIsSchemaViolation(t *testing.T) {
	cases := []struct {
		check string
		want  bool
	}{
		{"field_missing", true},
		{"type_drift", true},
		{"invalid_json", true},
		{"duplicate", false},
		{"out_of_order", false},
		{"lag", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsSchemaViolation(tc.check); got != tc.want {
			t.Errorf("IsSchemaViolation(%q) = %v, want %v", tc.check, got, tc.want)
		}
	}
}
