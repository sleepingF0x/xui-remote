package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// routeFirstMatch answers routeTest the way the core does for domain rules:
// the first rule with a matching domain: suffix wins, otherwise the traffic
// falls through to the first outbound.
func routeFirstMatch(h **harness) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/xray/routeTest" {
			return false
		}
		_ = r.ParseForm()
		domain := r.Form.Get("domain")
		var tpl struct {
			Outbounds []struct {
				Tag string `json:"tag"`
			} `json:"outbounds"`
			Routing struct {
				Rules []struct {
					Domain      []string `json:"domain"`
					OutboundTag string   `json:"outboundTag"`
				} `json:"rules"`
			} `json:"routing"`
		}
		_ = json.Unmarshal((*h).liveJSON(), &tpl)
		for _, rule := range tpl.Routing.Rules {
			for _, d := range rule.Domain {
				suffix := strings.TrimPrefix(d, "domain:")
				if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
					reply(w, map[string]any{"matched": true, "outboundTag": rule.OutboundTag})
					return true
				}
			}
		}
		reply(w, map[string]any{"matched": false, "outboundTag": tpl.Outbounds[0].Tag})
		return true
	}
}

func TestBroadNewRuleSwallowingOtherRulesIsReported(t *testing.T) {
	var h *harness
	h = newHarness(t, initial, routeFirstMatch(&h))
	h.send(1, "🔀 路由")
	h.press(1, "+ 新增规则")
	h.press(1, "B")
	h.press(1, "域名 / 分类")
	h.send(1, "domain:test")
	h.press(1, "最前（第 1 位）")
	h.contains(1, "已暂存")
	h.apply(1)

	report := h.last(1)
	for _, want := range []string{"1/1 通过", "流量去向变了", "a.test：A → <b>B</b>", "c.test：C → <b>B</b>"} {
		if !strings.Contains(report.Text, want) {
			t.Fatalf("report %q lacks %q", report.Text, want)
		}
	}
	// b.test already went to B, so it is not a drift.
	if strings.Contains(report.Text, "b.test：") {
		t.Fatalf("unchanged route reported as drift: %q", report.Text)
	}
	if button(report, "回滚这次改动") == "" {
		t.Fatal("collateral drift offers no rollback")
	}
}

func TestNarrowEditReportsUntouchedRulesUnchanged(t *testing.T) {
	var h *harness
	h = newHarness(t, initial, routeFirstMatch(&h))
	h.retarget(1, 2, "C")
	h.apply(1)
	report := h.last(1)
	if !strings.Contains(report.Text, "抽查 1 条未改动的规则，流量去向都没变") {
		t.Fatalf("report %q should say the rule below was rechecked", report.Text)
	}
	if strings.HasPrefix(report.Text, "⚠️") {
		t.Fatalf("clean apply flagged: %q", report.Text)
	}
}
