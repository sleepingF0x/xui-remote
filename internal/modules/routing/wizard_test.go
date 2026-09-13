package routing

import (
	"testing"

	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
)

// A bare string in an Xray routing rule is a substring match, while the same
// text in a geosite source file means a suffix match. Copying domains across
// without a prefix silently widens the rule, so every domain must come out
// explicitly prefixed.
func TestNormalizeValuesAlwaysPrefixesBareDomains(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		input string
		want  []string
	}{
		{"bare domain", "domain", "predict.fun", []string{"domain:predict.fun"}},
		{"strips www", "domain", "www.predict.fun", []string{"domain:predict.fun"}},
		{"uppercase folded", "domain", "Predict.FUN", []string{"domain:predict.fun"}},
		{"keeps full prefix", "domain", "full:a.b.com", []string{"full:a.b.com"}},
		{"keeps keyword prefix", "domain", "keyword:ads", []string{"keyword:ads"}},
		{"keeps regexp prefix", "domain", `regexp:^ads?[0-9]*\.`, []string{`regexp:^ads?[0-9]*\.`}},
		{"keeps geosite prefix", "domain", "geosite:cn", []string{"geosite:cn"}},
		{"keeps ext prefix", "domain", "ext:custom.dat:CRYPTO", []string{"ext:custom.dat:CRYPTO"}},
		{"ip values untouched", "ip", "1.1.1.1", []string{"1.1.1.1"}},
		{"cidr untouched", "ip", "10.0.0.0/8", []string{"10.0.0.0/8"}},
		{"geoip untouched", "ip", "geoip:cn", []string{"geoip:cn"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeValues(tt.kind, tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestNormalizeValuesSplitsOnEverySeparator(t *testing.T) {
	got := normalizeValues("domain", "a.com b.com,c.com\nd.com\t e.com")
	want := []string{"domain:a.com", "domain:b.com", "domain:c.com", "domain:d.com", "domain:e.com"}
	if len(got) != len(want) {
		t.Fatalf("got %d values %v, want %d", len(got), got, len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestNormalizeValuesDropsEmptyInput(t *testing.T) {
	if got := normalizeValues("domain", "  \n , \t "); len(got) != 0 {
		t.Fatalf("want no values, got %v", got)
	}
}

func liveRules() []xraycfg.Rule {
	mk := func(tag string, raw map[string]any) xraycfg.Rule {
		raw["outboundTag"] = tag
		return xraycfg.Rule{Raw: raw}
	}
	rules := []xraycfg.Rule{
		mk("api", map[string]any{"inboundTag": []any{"api"}}),
		mk("blocked", map[string]any{"ip": []any{"geoip:private"}}),
		mk("blocked", map[string]any{"protocol": []any{"bittorrent"}}),
		mk("us-vircs", map[string]any{"domain": []any{"domain:ip.sb"}}),
		mk("tw-gcp", map[string]any{"domain": []any{"ext:custom.dat:CRYPTO"}}),
	}
	for i := range rules {
		rules[i].Index = i
	}
	return rules
}

func TestValidatePositionAcceptsTheAllowedRange(t *testing.T) {
	rules := liveRules()
	tests := []struct {
		input string
		want  int
	}{
		{"4", 3},
		{" 5 ", 4},
		{"6", 5}, // append: one past the last rule
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := validatePosition(rules, tt.input)
			if err != nil {
				t.Fatalf("validatePosition(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("index = %d, want %d", got, tt.want)
			}
		})
	}
}

// Refusing beats clamping here: silently moving the rule elsewhere would leave
// the operator believing it landed where they asked.
func TestValidatePositionRefusesInfrastructurePrefixAndOutOfRange(t *testing.T) {
	rules := liveRules()
	for _, input := range []string{"1", "2", "3", "0", "-1", "7", "99", "abc", ""} {
		t.Run(input, func(t *testing.T) {
			if _, err := validatePosition(rules, input); err == nil {
				t.Fatalf("validatePosition(%q) must be refused", input)
			}
		})
	}
}

func TestValidatePositionAllowsTopWhenThereIsNoInfrastructurePrefix(t *testing.T) {
	rules := []xraycfg.Rule{{Raw: map[string]any{"domain": []any{"geosite:cn"}, "outboundTag": "direct"}}}
	got, err := validatePosition(rules, "1")
	if err != nil {
		t.Fatalf("validatePosition: %v", err)
	}
	if got != 0 {
		t.Fatalf("index = %d, want 0", got)
	}
}

// A move has no extra slot: the last valid target is the last existing rule,
// while an insert may take the position one past it.
func TestValidatePositionInDistinguishesInsertFromMove(t *testing.T) {
	rules := liveRules() // 5 rules, infrastructure floor at index 3
	if _, err := validatePositionIn(rules, "6", true); err != nil {
		t.Fatalf("insert at 6 (append) must be allowed: %v", err)
	}
	if _, err := validatePositionIn(rules, "6", false); err == nil {
		t.Fatal("move to 6 must be refused; there is no sixth rule")
	}
	got, err := validatePositionIn(rules, "5", false)
	if err != nil {
		t.Fatalf("move to 5: %v", err)
	}
	if got != 4 {
		t.Fatalf("index = %d, want 4", got)
	}
}

func TestValidatePositionInRefusesMovesIntoInfrastructurePrefix(t *testing.T) {
	rules := liveRules()
	for _, input := range []string{"1", "2", "3"} {
		if _, err := validatePositionIn(rules, input, false); err == nil {
			t.Fatalf("move to %s must be refused", input)
		}
	}
}

func TestMatchingFiltersOnWhatTheListShows(t *testing.T) {
	mk := func(i int, tag string, raw map[string]any) xraycfg.Rule {
		raw["outboundTag"] = tag
		return xraycfg.Rule{Index: i, Raw: raw}
	}
	rules := []xraycfg.Rule{
		mk(0, "api", map[string]any{"inboundTag": []any{"api"}}),
		mk(1, "us-vircs", map[string]any{"domain": []any{"ext:custom.dat:BANK-US"}}),
		mk(2, "hk-dmit", map[string]any{"domain": []any{"ext:custom.dat:CRYPTO-HK"}}),
		mk(3, "tw-gcp", map[string]any{"domain": []any{"geosite:category-cryptocurrency"}}),
	}
	tests := []struct {
		name    string
		filter  string
		wantIdx []int
	}{
		{"empty keeps everything", "", []int{0, 1, 2, 3}},
		{"by outbound tag", "hk-dmit", []int{2}},
		{"by domain fragment", "crypto", []int{2, 3}},
		{"case insensitive", "CRYPTO", []int{2, 3}},
		{"no match", "netflix", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matching(rules, tt.filter)
			if len(got) != len(tt.wantIdx) {
				t.Fatalf("matched %d rules, want %d", len(got), len(tt.wantIdx))
			}
			for i, r := range got {
				// Filtered rules must keep their real position: the number the
				// user types is the rule's index, not its place in the results.
				if r.Index != tt.wantIdx[i] {
					t.Fatalf("result %d has Index %d, want %d", i, r.Index, tt.wantIdx[i])
				}
			}
		})
	}
}

func TestDedupeKeepsOnlyValuesTheRuleLacks(t *testing.T) {
	tests := []struct {
		name       string
		existing   []string
		candidates []string
		want       []string
	}{
		{"all new", []string{"domain:a.com"}, []string{"domain:b.com"}, []string{"domain:b.com"}},
		{"exact duplicate", []string{"domain:a.com"}, []string{"domain:a.com"}, nil},
		// Routing tokens are matched case-insensitively by Xray, so re-adding
		// one in different case would otherwise grow the list silently.
		{"case duplicate", []string{"ext:custom.dat:CRYPTO"}, []string{"ext:custom.dat:crypto"}, nil},
		{"duplicate within input", nil, []string{"domain:a.com", "domain:a.com"}, []string{"domain:a.com"}},
		{"mixed", []string{"domain:a.com"}, []string{"domain:a.com", "domain:b.com"}, []string{"domain:b.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupe(tt.existing, tt.candidates)
			if len(got) != len(tt.want) {
				t.Fatalf("dedupe = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("dedupe = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestToAnyProducesTheShapeTheTemplateStores(t *testing.T) {
	got := toAny([]string{"a", "b"})
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	// The template is a map[string]any tree, so matchers must go back as []any
	// or the next read cannot parse them.
	for i, v := range got {
		if _, ok := v.(string); !ok {
			t.Fatalf("element %d is %T, want string", i, v)
		}
	}
}
