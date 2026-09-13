package client

import (
	"testing"
	"time"

	"github.com/lichenhao/xui-remote/internal/panel"
)

// A stash token is eight hex characters, which is also a plausible client
// name — so a miss that fell back to the raw argument rendered the token
// itself as the client ("👤 eccf030d"). The kind marker removes the guess.
func TestParseRefClassifiesEveryReferenceShape(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantKind  string
		wantValue string
		wantOK    bool
	}{
		{"stashed", []string{refStashed, "eccf030d"}, refStashed, "eccf030d", true},
		{"inline", []string{refInline, "alice@example.com"}, refInline, "alice@example.com", true},
		// An address may contain a colon; rejoining keeps it whole.
		{"inline with colon", []string{refInline, "weird", "name@example.com"}, refInline, "weird:name@example.com", true},
		// A bare token is what the old ambiguous form looked like.
		{"no kind marker", []string{"eccf030d"}, "", "", false},
		{"unknown kind", []string{"x", "alice@example.com"}, "", "", false},
		{"empty", nil, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, value, ok := parseRef(tt.args)
			if ok != tt.wantOK || kind != tt.wantKind || value != tt.wantValue {
				t.Fatalf("parseRef(%v) = (%q, %q, %v), want (%q, %q, %v)",
					tt.args, kind, value, ok, tt.wantKind, tt.wantValue, tt.wantOK)
			}
		})
	}
}

// The inline branch never touches the stash, so a nil Context proves it.
func TestResolveInlineNeedsNoStash(t *testing.T) {
	got, ok := resolve(nil, []string{refInline, "alice@example.com"})
	if !ok || got != "alice@example.com" {
		t.Fatalf("resolve = (%q, %v)", got, ok)
	}
	if _, ok := resolve(nil, []string{refInline, ""}); ok {
		t.Fatal("an empty inline value must be rejected, not passed on as a client")
	}
}

func TestInlineRefRoundTrips(t *testing.T) {
	kind, value, ok := parseRef([]string{inlineRef("alice@example.com")[:1], "alice@example.com"})
	if !ok || kind != refInline || value != "alice@example.com" {
		t.Fatalf("parseRef = (%q, %q, %v)", kind, value, ok)
	}
}

func TestSlimExpiryReadsAsTimeLeft(t *testing.T) {
	tests := []struct {
		name   string
		millis int64
		want   string
	}{
		{"never", 0, "不过期"},
		{"past", time.Now().Add(-24 * time.Hour).UnixMilli(), "已过期"},
		{"future", time.Now().Add(72 * time.Hour).UnixMilli(), "剩 2 天"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slimExpiry(panel.ClientSlim{ExpiryTime: tt.millis}); got != tt.want {
				t.Fatalf("slimExpiry() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlimTrafficDistinguishesUnlimited(t *testing.T) {
	withCap := panel.ClientSlim{TotalGB: 1 << 30, Traffic: &panel.ClientTraffic{Up: 1 << 29, Down: 0}}
	if got := slimTraffic(withCap); got != "512.00 MB / 1.00 GB" {
		t.Fatalf("capped = %q", got)
	}
	uncapped := panel.ClientSlim{Traffic: &panel.ClientTraffic{Up: 1 << 20}}
	if got := slimTraffic(uncapped); got != "1.00 MB / 不限" {
		t.Fatalf("uncapped = %q", got)
	}
	// A client the panel reported no counters for must not panic.
	if got := slimTraffic(panel.ClientSlim{}); got != "0 B / 不限" {
		t.Fatalf("no traffic row = %q", got)
	}
}
