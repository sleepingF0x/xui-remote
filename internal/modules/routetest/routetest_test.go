package routetest

import (
	"strings"
	"testing"
)

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

// "改用 UDP" scanned as "改用户" — a different control on the same screen.
// The label has to read as a setting, like the port buttons beside it.
func TestNetLabelReadsAsASettingNotAVerb(t *testing.T) {
	p := probe{Network: "tcp"}
	if got := netLabel(p, "tcp"); got != "• 协议 TCP" {
		t.Fatalf("active protocol label = %q", got)
	}
	if got := netLabel(p, "udp"); got != "协议 UDP" {
		t.Fatalf("inactive protocol label = %q", got)
	}
	for _, network := range []string{"tcp", "udp"} {
		if strings.HasPrefix(strings.TrimPrefix(netLabel(p, network), "• "), "改") {
			t.Fatalf("label for %q still reads as a verb", network)
		}
	}
}

func TestNarrowsTreatsEmptyValuesAsNoConstraint(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want bool
	}{
		{"absent", nil, false},
		{"empty string", "", false},
		{"empty list", []any{}, false},
		{"port string", "443", true},
		{"user list", []any{"admin"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := narrows(tt.v); got != tt.want {
				t.Fatalf("narrows(%#v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

// A parameter no rule reads cannot change the answer, so the screen says so
// instead of leaving a control whose purpose has to be guessed.
func TestUnusedNoteNamesOnlyTheIgnoredParameters(t *testing.T) {
	tests := []struct {
		name string
		d    discriminators
		want string
	}{
		{"neither used", discriminators{known: true}, "当前规则不区分端口和协议，改这些不会影响结果。"},
		{"port used", discriminators{known: true, port: 2}, "当前规则不区分协议，改这些不会影响结果。"},
		{"network used", discriminators{known: true, network: 1}, "当前规则不区分端口，改这些不会影响结果。"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unusedNote(tt.d); got != tt.want {
				t.Fatalf("unusedNote() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAnyUnusedStaysQuietWhenTheConfigCouldNotBeRead(t *testing.T) {
	// An unreadable template must not claim the rules ignore anything.
	if (discriminators{}).anyUnused() {
		t.Fatal("an unknown rule set must make no claim about unused parameters")
	}
	if (discriminators{known: true, port: 1, network: 1}).anyUnused() {
		t.Fatal("nothing is unused when both are read by some rule")
	}
}
