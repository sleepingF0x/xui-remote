package detect

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKind  Kind
		wantValue string
	}{
		{"plain domain", "predict.fun", KindDomain, "predict.fun"},
		{"strips www", "www.predict.fun", KindDomain, "predict.fun"},
		{"url reduces to host", "https://predict.fun/trade?x=1", KindURL, "predict.fun"},
		{"url with www", "https://www.example.com/", KindURL, "example.com"},
		{"ipv4", "1.1.1.1", KindIP, "1.1.1.1"},
		{"ipv6", "2606:4700:4700::1111", KindIP, "2606:4700:4700::1111"},
		{"cidr", "10.0.0.0/8", KindCIDR, "10.0.0.0/8"},
		// x-ui client identifiers are emails and contain a dot, so they would
		// otherwise be misread as domains and route-tested instead of looked up.
		{"email", "alice@example.com", KindEmail, "alice@example.com"},
		{"bare label is not a domain", "admin", Unknown, "admin"},
		{"empty", "   ", Unknown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.input)
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.Value != tt.wantValue {
				t.Fatalf("Value = %q, want %q", got.Value, tt.wantValue)
			}
		})
	}
}

func TestIsHostLike(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"example.com", true},
		{"1.1.1.1", true},
		{"https://example.com", true},
		{"alice@example.com", false},
		{"10.0.0.0/8", false},
		{"admin", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Classify(tt.input).IsHostLike(); got != tt.want {
				t.Fatalf("IsHostLike(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
