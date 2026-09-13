package txn

import (
	"encoding/json"
	"testing"
)

// Apply re-serializes the template, so a byte comparison would report a
// conflict on every second apply. Conflict detection has to compare by value.
func TestSameJSONIgnoresKeyOrderAndFormatting(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", `{"a":1}`, `{"a":1}`, true},
		{"key order", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"whitespace", `{"a": 1}`, "{\n  \"a\": 1\n}", true},
		{"nested key order", `{"r":{"x":1,"y":2}}`, `{"r":{"y":2,"x":1}}`, true},
		{"different value", `{"a":1}`, `{"a":2}`, false},
		{"added key", `{"a":1}`, `{"a":1,"b":2}`, false},
		{"array order matters", `{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{"invalid json is never equal", `{`, `{`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sameJSON(json.RawMessage(tt.a), json.RawMessage(tt.b))
			if got != tt.want {
				t.Fatalf("sameJSON(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
