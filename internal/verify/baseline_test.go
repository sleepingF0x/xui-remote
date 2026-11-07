package verify

import (
	"encoding/json"
	"reflect"
	"testing"
)

const threeRules = `{"outbounds":[{"tag":"A"},{"tag":"B"}],"routing":{"rules":[
{"type":"field","domain":["domain:a.test"],"outboundTag":"A"},
{"type":"field","domain":["domain:b.test"],"outboundTag":"B"},
{"type":"field","domain":["domain:c.test"],"outboundTag":"A"}]}}`

func indices(t *testing.T, base, draft string) []int {
	t.Helper()
	rules, err := collateralRules(json.RawMessage(base), json.RawMessage(draft))
	if err != nil {
		t.Fatal(err)
	}
	out := []int{}
	for _, r := range rules {
		out = append(out, r.Index)
	}
	return out
}

// Routing is first-match: only rules at or below an edit can lose traffic to
// it, so probing the ones above would spend probes on rules that cannot move.
func TestCollateralRulesAreTheUntouchedOnesBelowTheEdit(t *testing.T) {
	retargeted := `{"outbounds":[{"tag":"A"},{"tag":"B"}],"routing":{"rules":[
{"type":"field","domain":["domain:a.test"],"outboundTag":"A"},
{"type":"field","domain":["domain:b.test"],"outboundTag":"A"},
{"type":"field","domain":["domain:c.test"],"outboundTag":"A"}]}}`
	if got := indices(t, threeRules, retargeted); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("retarget middle rule: got %v, want [2]", got)
	}

	inserted := `{"outbounds":[{"tag":"A"},{"tag":"B"}],"routing":{"rules":[
{"type":"field","domain":["domain:test"],"outboundTag":"B"},
{"type":"field","domain":["domain:a.test"],"outboundTag":"A"},
{"type":"field","domain":["domain:b.test"],"outboundTag":"B"},
{"type":"field","domain":["domain:c.test"],"outboundTag":"A"}]}}`
	if got := indices(t, threeRules, inserted); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("insert at top: got %v, want [1 2 3]", got)
	}
}

// Changing an outbound or another routing setting can move traffic of any
// rule, so the ones above the edit are probed too — after the nearer ones.
func TestNonRuleChangesWidenTheSampleToEveryRule(t *testing.T) {
	draft := `{"outbounds":[{"tag":"A"},{"tag":"B","protocol":"freedom"}],"routing":{"rules":[
{"type":"field","domain":["domain:a.test"],"outboundTag":"A"},
{"type":"field","domain":["domain:b.test"],"outboundTag":"B"},
{"type":"field","domain":["domain:c.test"],"outboundTag":"B"}]}}`
	if got := indices(t, threeRules, draft); !reflect.DeepEqual(got, []int{1, 0}) {
		t.Fatalf("outbound change: got %v, want [1 0]", got)
	}
}

func TestRouteKeyIgnoresTheNodeABalancerPicked(t *testing.T) {
	a := route{matched: true, outbound: "node-1", groups: []string{"auto"}}
	b := route{matched: true, outbound: "node-2", groups: []string{"auto"}}
	if a.key() != b.key() {
		t.Fatal("same balancer path reported as a drift")
	}
	if (route{matched: true, outbound: "A"}).key() == (route{matched: false, outbound: "A"}).key() {
		t.Fatal("falling through to the default outbound must differ from matching a rule")
	}
}
