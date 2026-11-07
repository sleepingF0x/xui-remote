package xraycfg

import (
	"encoding/json"
	"reflect"
	"testing"
)

const rebaseBase = `{"log":{"loglevel":"warning"},"outbounds":[{"tag":"direct"}],
"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","domain":["domain:a.test"],"outboundTag":"direct"}]}}`

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRebaseKeepsBothSidesOfDisjointEdits(t *testing.T) {
	ours := `{"log":{"loglevel":"warning"},"outbounds":[{"tag":"direct"}],
"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","domain":["domain:a.test"],"outboundTag":"direct"},{"type":"field","domain":["domain:b.test"],"outboundTag":"direct"}]}}`
	theirs := `{"log":{"loglevel":"info"},"outbounds":[{"tag":"direct"},{"tag":"warp"}],
"routing":{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","domain":["domain:a.test"],"outboundTag":"direct"}]}}`
	merged, took, conflicts, err := Rebase(json.RawMessage(rebaseBase), json.RawMessage(ours), json.RawMessage(theirs))
	if err != nil || conflicts != nil {
		t.Fatalf("err %v conflicts %v", err, conflicts)
	}
	if want := []string{"log", "outbounds", "routing.domainStrategy"}; !reflect.DeepEqual(took, want) {
		t.Fatalf("taken from theirs %v, want %v", took, want)
	}
	m := decode(t, merged)
	if m["log"].(map[string]any)["loglevel"] != "info" || len(m["outbounds"].([]any)) != 2 {
		t.Fatalf("their edits lost: %s", merged)
	}
	routing := m["routing"].(map[string]any)
	if routing["domainStrategy"] != "IPIfNonMatch" || len(routing["rules"].([]any)) != 2 {
		t.Fatalf("a side lost under routing: %s", merged)
	}
	if RebasedRules(took) {
		t.Fatal("rules came from ours")
	}
}

// Rule order is routing semantics, so two edited rule lists are a conflict
// rather than something to interleave.
func TestRebaseRefusesTwoEditedRuleLists(t *testing.T) {
	ours := `{"log":{"loglevel":"warning"},"outbounds":[{"tag":"direct"}],"routing":{"domainStrategy":"AsIs","rules":[]}}`
	theirs := `{"log":{"loglevel":"info"},"outbounds":[{"tag":"direct"}],
"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","domain":["domain:web.test"],"outboundTag":"direct"}]}}`
	merged, took, conflicts, err := Rebase(json.RawMessage(rebaseBase), json.RawMessage(ours), json.RawMessage(theirs))
	if err != nil || merged != nil || took != nil {
		t.Fatalf("merged despite conflict: %s %v %v", merged, took, err)
	}
	if !reflect.DeepEqual(conflicts, []string{"routing.rules"}) {
		t.Fatalf("conflicts %v", conflicts)
	}
}

func TestRebaseTakesTheirRulesAndDeletionsWhenOursLeftThemAlone(t *testing.T) {
	ours := `{"log":{"loglevel":"error"},"outbounds":[{"tag":"direct"}],
"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","domain":["domain:a.test"],"outboundTag":"direct"}]}}`
	theirs := `{"outbounds":[{"tag":"direct"}],"routing":{"domainStrategy":"AsIs","rules":[]}}`
	_, _, conflicts, _ := Rebase(json.RawMessage(rebaseBase), json.RawMessage(ours), json.RawMessage(theirs))
	if !reflect.DeepEqual(conflicts, []string{"log"}) {
		t.Fatalf("their deletion of an edited key must conflict, got %v", conflicts)
	}

	ours = rebaseBase
	merged, took, conflicts, err := Rebase(json.RawMessage(rebaseBase), json.RawMessage(ours), json.RawMessage(theirs))
	if err != nil || conflicts != nil {
		t.Fatalf("err %v conflicts %v", err, conflicts)
	}
	if _, ok := decode(t, merged)["log"]; ok {
		t.Fatalf("their deletion was not kept: %s", merged)
	}
	if !RebasedRules(took) {
		t.Fatalf("rules should come from theirs: %v", took)
	}
}

func TestRebaseAcceptsTheSameEditOnBothSides(t *testing.T) {
	both := `{"log":{"loglevel":"info"},"outbounds":[{"tag":"direct"}],
"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","domain":["domain:a.test"],"outboundTag":"direct"}]}}`
	_, took, conflicts, err := Rebase(json.RawMessage(rebaseBase), json.RawMessage(both), json.RawMessage(both))
	if err != nil || conflicts != nil || took != nil {
		t.Fatalf("identical edits: took %v conflicts %v err %v", took, conflicts, err)
	}
}
