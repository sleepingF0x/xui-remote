package xraycfg

import "testing"

func rule(tag string, raw map[string]any) Rule {
	if raw == nil {
		raw = map[string]any{}
	}
	raw["outboundTag"] = tag
	return Rule{Raw: raw}
}

func tags(rules []Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.Target()
	}
	return out
}

func TestInsertPlacesRuleAndReindexes(t *testing.T) {
	base := []Rule{rule("a", nil), rule("b", nil), rule("c", nil)}
	tests := []struct {
		name string
		at   int
		want []string
	}{
		{"top", 0, []string{"new", "a", "b", "c"}},
		{"middle", 2, []string{"a", "b", "new", "c"}},
		{"end", 3, []string{"a", "b", "c", "new"}},
		{"past end clamps", 99, []string{"a", "b", "c", "new"}},
		{"negative clamps", -5, []string{"new", "a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Insert(append([]Rule{}, base...), tt.at, rule("new", nil))
			if g := tags(got); !equal(g, tt.want) {
				t.Fatalf("order = %v, want %v", g, tt.want)
			}
			for i, r := range got {
				if r.Index != i {
					t.Fatalf("rule %d carries Index %d", i, r.Index)
				}
			}
		})
	}
}

func TestMoveReordersWithoutLosingRules(t *testing.T) {
	base := []Rule{rule("a", nil), rule("b", nil), rule("c", nil), rule("d", nil)}
	tests := []struct {
		name     string
		from, to int
		want     []string
	}{
		{"down one", 0, 1, []string{"b", "a", "c", "d"}},
		{"up one", 3, 2, []string{"a", "b", "d", "c"}},
		{"to top", 2, 0, []string{"c", "a", "b", "d"}},
		{"past end clamps", 0, 99, []string{"b", "c", "d", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Move(append([]Rule{}, base...), tt.from, tt.to)
			if err != nil {
				t.Fatalf("Move: %v", err)
			}
			if g := tags(got); !equal(g, tt.want) {
				t.Fatalf("order = %v, want %v", g, tt.want)
			}
		})
	}
}

func TestMoveRejectsUnknownSource(t *testing.T) {
	_, err := Move([]Rule{rule("a", nil)}, 5, 0)
	if err == nil {
		t.Fatal("moving a nonexistent rule must fail, not silently no-op")
	}
}

func TestIsCatchAllOnlyForUnnarrowedRules(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"bare outbound", map[string]any{}, true},
		{"network only", map[string]any{"network": "tcp,udp"}, true},
		// The api inbound's rule is inboundTag-only; treating it as a catch-all
		// made SuggestPosition push every new rule to the very top.
		{"inboundTag only is scoped, not catch-all", map[string]any{"inboundTag": []any{"api"}}, false},
		{"has domain", map[string]any{"domain": []any{"geosite:cn"}}, false},
		{"has ip", map[string]any{"ip": []any{"geoip:cn"}}, false},
		{"has port", map[string]any{"port": "443"}, false},
		{"empty domain list does not narrow", map[string]any{"domain": []any{}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rule("x", tt.raw).IsCatchAll(); got != tt.want {
				t.Fatalf("IsCatchAll() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuggestPositionLandsBeforeFirstCatchAll(t *testing.T) {
	rules := []Rule{
		rule("block", map[string]any{"domain": []any{"geosite:ads"}}),
		rule("direct", map[string]any{"ip": []any{"geoip:cn"}}),
		rule("proxy", map[string]any{"network": "tcp,udp"}),
		rule("never", map[string]any{"domain": []any{"geosite:netflix"}}),
	}
	if got := SuggestPosition(rules); got != 2 {
		t.Fatalf("SuggestPosition() = %d, want 2 (before the catch-all)", got)
	}
}

func TestSuggestPositionAppendsWhenNoCatchAll(t *testing.T) {
	rules := []Rule{rule("a", map[string]any{"domain": []any{"x"}})}
	if got := SuggestPosition(rules); got != 1 {
		t.Fatalf("SuggestPosition() = %d, want 1", got)
	}
}

// Shape taken from a live panel: infrastructure rules first, then specific
// domain rules, no catch-all anywhere. A new rule belongs at the end.
func TestSuggestPositionOnRuleSetWithoutCatchAll(t *testing.T) {
	rules := []Rule{
		rule("api", map[string]any{"inboundTag": []any{"api"}}),
		rule("blocked", map[string]any{"ip": []any{"geoip:private"}}),
		rule("blocked", map[string]any{"protocol": []any{"bittorrent"}}),
		rule("direct", map[string]any{"domain": []any{"geosite:openai"}, "user": []any{"a@b"}}),
		rule("tw-gcp", map[string]any{"domain": []any{"ext:custom.dat:CRYPTO"}}),
	}
	if got := SuggestPosition(rules); got != len(rules) {
		t.Fatalf("SuggestPosition() = %d, want %d (append; nothing shadows)", got, len(rules))
	}
}

func TestRulesRoundTripsThroughTemplate(t *testing.T) {
	tpl := map[string]any{"routing": map[string]any{"rules": []any{
		map[string]any{"outboundTag": "a", "domain": []any{"example.com"}},
	}}}
	rules, err := Rules(tpl)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	SetRules(tpl, Insert(rules, 0, rule("b", nil)))
	after, err := Rules(tpl)
	if err != nil {
		t.Fatalf("Rules after write: %v", err)
	}
	if g := tags(after); !equal(g, []string{"b", "a"}) {
		t.Fatalf("order after write = %v", g)
	}
}

func TestRulesOnTemplateWithoutRoutingSection(t *testing.T) {
	rules, err := Rules(map[string]any{})
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("want no rules, got %d", len(rules))
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Layout taken from a live panel. The api inbound listens on 127.0.0.1, so its
// own traffic matches geoip:private — rule 1 must stay above rule 2 or the
// panel's gRPC API gets blocked. Nothing may be inserted into that prefix.
func liveInfraRules() []Rule {
	return []Rule{
		rule("api", map[string]any{"inboundTag": []any{"api"}, "ruleTag": "system-api"}),
		rule("blocked", map[string]any{"ip": []any{"geoip:private"}, "ruleTag": "block-private"}),
		rule("blocked", map[string]any{"protocol": []any{"bittorrent"}, "ruleTag": "block-bittorrent"}),
		rule("us-vircs", map[string]any{"domain": []any{"domain:ip.sb"}, "user": []any{"admin"}}),
		rule("tw-gcp", map[string]any{"domain": []any{"ext:custom.dat:CRYPTO"}}),
	}
}

func TestSafeFloorSkipsInfrastructurePrefix(t *testing.T) {
	if got := SafeFloor(liveInfraRules()); got != 3 {
		t.Fatalf("SafeFloor() = %d, want 3 (first rule carrying a domain matcher)", got)
	}
}

func TestSafeFloorIsZeroWhenFirstRuleIsABusinessRule(t *testing.T) {
	rules := []Rule{rule("a", map[string]any{"domain": []any{"geosite:cn"}})}
	if got := SafeFloor(rules); got != 0 {
		t.Fatalf("SafeFloor() = %d, want 0", got)
	}
}

func TestClampPositionRefusesTheInfrastructurePrefix(t *testing.T) {
	rules := liveInfraRules()
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"above api rule", 0, 3},
		{"between api and private block", 1, 3},
		{"at the floor", 3, 3},
		{"below the floor", 4, 4},
		{"past the end clamps to append", 99, len(rules)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampPosition(rules, tt.in); got != tt.want {
				t.Fatalf("ClampPosition(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestSuggestPositionNeverLandsInsideTheInfrastructurePrefix(t *testing.T) {
	// An inboundTag-scoped rule sits first; a real catch-all sits last.
	rules := append(liveInfraRules(), rule("proxy", map[string]any{"network": "tcp,udp"}))
	got := SuggestPosition(rules)
	if floor := SafeFloor(rules); got < floor {
		t.Fatalf("SuggestPosition() = %d, below the floor %d", got, floor)
	}
	if got != 5 {
		t.Fatalf("SuggestPosition() = %d, want 5 (before the catch-all)", got)
	}
}

// A rule carrying both an outboundTag and a balancerTag is ambiguous, so
// retargeting has to clear whichever field it is not setting.
func TestSetTargetClearsTheOtherTargetingField(t *testing.T) {
	t.Run("outbound replaces balancer", func(t *testing.T) {
		r := Rule{Raw: map[string]any{"balancerTag": "auto", "domain": []any{"x"}}}
		r.SetTarget("hk-dmit", false)
		if got := r.Raw["outboundTag"]; got != "hk-dmit" {
			t.Fatalf("outboundTag = %v", got)
		}
		if _, still := r.Raw["balancerTag"]; still {
			t.Fatal("balancerTag must be cleared")
		}
		if got := r.Target(); got != "hk-dmit" {
			t.Fatalf("Target() = %q", got)
		}
	})
	t.Run("balancer replaces outbound", func(t *testing.T) {
		r := Rule{Raw: map[string]any{"outboundTag": "hk-dmit"}}
		r.SetTarget("auto", true)
		if got := r.Raw["balancerTag"]; got != "auto" {
			t.Fatalf("balancerTag = %v", got)
		}
		if _, still := r.Raw["outboundTag"]; still {
			t.Fatal("outboundTag must be cleared")
		}
	})
}

func TestMatchValuesReportsTheFieldTheRuleActuallyUses(t *testing.T) {
	tests := []struct {
		name      string
		raw       map[string]any
		wantField string
		wantCount int
	}{
		{"domain rule", map[string]any{"domain": []any{"geosite:cn", "domain:a.com"}}, "domain", 2},
		{"ip rule", map[string]any{"ip": []any{"geoip:cn"}}, "ip", 1},
		{"no matchers", map[string]any{"network": "tcp"}, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, values := Rule{Raw: tt.raw}.MatchValues()
			if field != tt.wantField || len(values) != tt.wantCount {
				t.Fatalf("MatchValues() = (%q, %d values), want (%q, %d)",
					field, len(values), tt.wantField, tt.wantCount)
			}
		})
	}
}

func TestBalancerTagsReadsRoutingBalancers(t *testing.T) {
	tpl := map[string]any{"routing": map[string]any{"balancers": []any{
		map[string]any{"tag": "auto", "selector": []any{"proxy"}},
		map[string]any{"selector": []any{"x"}}, // no tag: not targetable
	}}}
	got := BalancerTags(tpl)
	if len(got) != 1 || got[0] != "auto" {
		t.Fatalf("BalancerTags() = %v, want [auto]", got)
	}
	if BalancerTags(map[string]any{}) != nil {
		t.Fatal("a template without routing must yield no balancers")
	}
}
