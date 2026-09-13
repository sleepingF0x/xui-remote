package routetest

import "testing"

// The result has to state what it was produced under: half this panel's rules
// are user-scoped, so an unexpected answer is usually the parameters, not the
// rules.
func TestDescribeSpellsOutEveryParameter(t *testing.T) {
	tests := []struct {
		name string
		p    probe
		want string
	}{
		{"defaults", probe{Network: "tcp", Port: 443}, "TCP · 端口 443 · 不指定用户"},
		{"udp", probe{Network: "udp", Port: 53}, "UDP · 端口 53 · 不指定用户"},
		{"with user", probe{Network: "tcp", Port: 443, Email: "a@b.c"}, "TCP · 端口 443 · 用户 a@b.c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describe(tt.p); got != tt.want {
				t.Fatalf("describe() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPortLabelMarksTheActivePort(t *testing.T) {
	p := probe{Port: 443}
	if got := portLabel(p, 443); got != "• 端口 443" {
		t.Fatalf("active port label = %q", got)
	}
	if got := portLabel(p, 80); got != "端口 80" {
		t.Fatalf("inactive port label = %q", got)
	}
}

func TestProbeSurvivesAStashRoundTrip(t *testing.T) {
	want := probe{Target: "predict.fun", Port: 8443, Network: "udp", Email: "alice@example.com"}
	got, err := decodeProbe(want.encode())
	if err != nil {
		t.Fatalf("decodeProbe: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}
