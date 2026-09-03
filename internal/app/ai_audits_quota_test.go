package app

import "testing"

func TestValidateAIVisibilityAuditMonthlyLimit(t *testing.T) {
	tests := []struct {
		name    string
		limit   int32
		wantErr bool
	}{
		{"zero disables audits", 0, false},
		{"positive limit", 10, false},
		{"negative rejected", -1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAIVisibilityAuditMonthlyLimit(test.limit)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateAIVisibilityAuditMonthlyLimit(%d) error = %v, wantErr %v", test.limit, err, test.wantErr)
			}
		})
	}
}
