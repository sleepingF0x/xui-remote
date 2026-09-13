package verify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lichenhao/xui-remote/internal/panel"
	"github.com/lichenhao/xui-remote/internal/state"
)

func TestTestableEntryPicksProbeableMembers(t *testing.T) {
	tests := []struct {
		name  string
		entry panel.GeoEntry
		isIP  bool
		want  string
	}{
		{"suffix domain", panel.GeoEntry{Kind: "domain", Value: "example.com"}, false, "example.com"},
		{"exact domain", panel.GeoEntry{Kind: "full", Value: "a.example.com"}, false, "a.example.com"},
		{"keyword unusable", panel.GeoEntry{Kind: "keyword", Value: "ads"}, false, ""},
		{"regexp unusable", panel.GeoEntry{Kind: "regexp", Value: `^ads?\.`}, false, ""},
		{"plain ip", panel.GeoEntry{Kind: "cidr", Value: "1.1.1.1"}, true, "1.1.1.1"},
		// A CIDR's network address is inside the range, so it is a valid probe.
		{"cidr uses network address", panel.GeoEntry{Kind: "cidr", Value: "10.0.0.0/8"}, true, "10.0.0.0"},
		{"domain is not an ip probe", panel.GeoEntry{Kind: "domain", Value: "example.com"}, true, ""},
		{"empty value", panel.GeoEntry{Kind: "domain", Value: ""}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := testableEntry(tt.entry, tt.isIP); got != tt.want {
				t.Fatalf("testableEntry(%+v, %v) = %q, want %q", tt.entry, tt.isIP, got, tt.want)
			}
		})
	}
}

func TestSplitExt(t *testing.T) {
	file, code, ok := splitExt("ext:custom.dat:CRYPTO-HK")
	if !ok || file != "custom.dat" || code != "CRYPTO-HK" {
		t.Fatalf("splitExt = (%q, %q, %v)", file, code, ok)
	}
	if _, _, ok := splitExt("ext:custom.dat"); ok {
		t.Fatal("a two-part ext token must not parse")
	}
}

func TestResolveLiteralsWithoutTouchingThePanel(t *testing.T) {
	// A nil client proves these paths never make a call.
	v := New(nil)
	tests := []struct {
		name       string
		check      state.Check
		wantTarget string
		wantSkip   bool
	}{
		{"domain prefix", state.Check{Kind: "domain", Value: "domain:predict.fun"}, "predict.fun", false},
		{"full prefix", state.Check{Kind: "domain", Value: "full:a.b.com"}, "a.b.com", false},
		{"bare domain", state.Check{Kind: "domain", Value: "predict.fun"}, "predict.fun", false},
		{"keyword skipped", state.Check{Kind: "domain", Value: "keyword:ads"}, "", true},
		{"regexp skipped", state.Check{Kind: "domain", Value: `regexp:^a\.`}, "", true},
		{"literal ip", state.Check{Kind: "ip", Value: "1.1.1.1"}, "1.1.1.1", false},
		{"cidr skipped", state.Check{Kind: "ip", Value: "10.0.0.0/8"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, reason := v.resolve(tt.check)
			if tt.wantSkip {
				if target != "" || reason == "" {
					t.Fatalf("want a skip with a reason, got target=%q reason=%q", target, reason)
				}
				return
			}
			if target != tt.wantTarget {
				t.Fatalf("target = %q, want %q", target, tt.wantTarget)
			}
		})
	}
}

// fakePanel serves the two endpoints a verification round needs.
func fakePanel(t *testing.T, entries []panel.GeoEntry, outboundFor map[string]string) *panel.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(obj string) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"obj":` + obj + `}`))
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/geodata/entries"):
			if r.URL.Query().Get("file") == "" {
				// The panel rejects an empty file name outright.
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":false,"msg":"invalid geodata file name"}`))
				return
			}
			raw, _ := json.Marshal(entries)
			write(`{"total":` + itoa(len(entries)) + `,"items":` + string(raw) + `}`)
		case strings.HasSuffix(r.URL.Path, "/routeTest"):
			_ = r.ParseForm()
			key := r.PostForm.Get("domain") + r.PostForm.Get("ip")
			if u := r.PostForm.Get("email"); u != "" {
				key += "@" + u
			}
			tag, ok := outboundFor[key]
			if !ok {
				write(`{"matched":false}`)
				return
			}
			write(`{"matched":true,"outboundTag":"` + tag + `"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return panel.New(panel.Options{BaseURL: srv.URL + "/api/", Token: "t", Timeout: 5 * time.Second})
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func TestRunReportsAMatchingRuleAsPassed(t *testing.T) {
	v := New(fakePanel(t, nil, map[string]string{"predict.fun": "hk-dmit"}))
	got := v.Run([]state.Check{{Kind: "domain", Value: "domain:predict.fun", Expect: "hk-dmit", Index: 5}})
	if len(got) != 1 {
		t.Fatalf("want 1 result, got %d", len(got))
	}
	if !got[0].Verified() || !got[0].OK {
		t.Fatalf("result = %+v, want a passing check", got[0])
	}
}

// The whole point: a rule can be written successfully and still never fire.
func TestRunReportsAShadowedRuleAsFailed(t *testing.T) {
	v := New(fakePanel(t, nil, map[string]string{"predict.fun": "tw-gcp"}))
	got := v.Run([]state.Check{{Kind: "domain", Value: "domain:predict.fun", Expect: "hk-dmit", Index: 5}})
	if !got[0].Verified() {
		t.Fatalf("check must be verifiable: %+v", got[0])
	}
	if got[0].OK {
		t.Fatal("a rule routed to the wrong outbound must not pass")
	}
	if got[0].Actual != "tw-gcp" || got[0].Expect != "hk-dmit" {
		t.Fatalf("result = %+v", got[0])
	}
}

// A geosite: token carries no file name, and the panel refuses an empty one —
// the default database has to be filled in.
func TestRunNamesTheDefaultDatabaseForABarePrefix(t *testing.T) {
	entries := []panel.GeoEntry{{Kind: "domain", Value: "sampled.example"}}
	v := New(fakePanel(t, entries, map[string]string{"sampled.example": "direct"}))
	got := v.Run([]state.Check{{Kind: "domain", Value: "geosite:anthropic", Expect: "direct"}})
	if !got[0].Verified() {
		t.Fatalf("geosite: lookup must name geosite.dat, got skip %q", got[0].Skipped)
	}
	if !got[0].OK {
		t.Fatalf("result = %+v", got[0])
	}
}

func TestRunSamplesACategoryMemberToProbe(t *testing.T) {
	// The first two members are unusable, so resolution must keep looking.
	entries := []panel.GeoEntry{
		{Kind: "keyword", Value: "ads"},
		{Kind: "regexp", Value: `^x\.`},
		{Kind: "domain", Value: "sampled.example"},
	}
	v := New(fakePanel(t, entries, map[string]string{"sampled.example": "hk-dmit"}))
	got := v.Run([]state.Check{{Kind: "domain", Value: "ext:custom.dat:CRYPTO-HK", Expect: "hk-dmit"}})
	if !got[0].Verified() {
		t.Fatalf("want a verified check, got skip %q", got[0].Skipped)
	}
	if got[0].Target != "sampled.example" {
		t.Fatalf("Target = %q, want the first probeable member", got[0].Target)
	}
	if !got[0].OK {
		t.Fatalf("result = %+v", got[0])
	}
}

func TestRunSkipsACategoryWithNothingProbeable(t *testing.T) {
	v := New(fakePanel(t, []panel.GeoEntry{{Kind: "keyword", Value: "ads"}, {Kind: "regexp", Value: "^x."}}, nil))
	got := v.Run([]state.Check{{Kind: "domain", Value: "geosite:onlykeywords", Expect: "x"}})
	if got[0].Verified() {
		t.Fatal("a category with no probeable member must be skipped, not failed")
	}
	if !strings.Contains(got[0].Skipped, "onlykeywords") {
		t.Fatalf("skip reason should name the category: %q", got[0].Skipped)
	}
}

func TestRunTreatsNoMatchAsAFailureNotASkip(t *testing.T) {
	v := New(fakePanel(t, nil, nil)) // routeTest answers matched:false
	got := v.Run([]state.Check{{Kind: "domain", Value: "domain:nowhere.example", Expect: "hk-dmit"}})
	if !got[0].Verified() {
		t.Fatalf("an answered probe is a real result, not a skip: %q", got[0].Skipped)
	}
	if got[0].OK {
		t.Fatal("falling through to the default outbound is not a pass")
	}
}

// A rule narrowed by user is unreachable to a probe that names none, so
// verifying a retarget of one reported a failure that was not one.
func TestRunProbesAsTheRulesUser(t *testing.T) {
	// Only the identity-qualified key resolves, mirroring the live panel.
	v := New(fakePanel(t, nil, map[string]string{"twitter.jp@admin": "us-vircs"}))

	withUser := v.Run([]state.Check{{
		Kind: "domain", Value: "domain:twitter.jp", Expect: "us-vircs", User: "admin",
	}})
	if !withUser[0].OK {
		t.Fatalf("probe as the rule's user must match: %+v", withUser[0])
	}
	if withUser[0].User != "admin" {
		t.Fatalf("result must record the identity it ran as, got %q", withUser[0].User)
	}

	withoutUser := v.Run([]state.Check{{
		Kind: "domain", Value: "domain:twitter.jp", Expect: "us-vircs",
	}})
	if withoutUser[0].OK {
		t.Fatal("without the identity the rule cannot match; this is the old false alarm")
	}
}
