package xraycfg

import (
	"encoding/json"
	"reflect"
	"sort"
)

// Rebase replays the change from base to ours on top of theirs, a template
// that moved on independently (someone saved in the web panel, or the panel
// rotated an outbound by itself). It works per region: each top-level key,
// and each key under routing, is one region. A region only one side changed
// takes that side; one both changed identically is fine; one both changed
// differently is a conflict and nothing is merged.
//
// Lists are never merged element by element. Rule order decides routing, so
// interleaving two edited rule lists could produce an order neither side chose.
//
// fromTheirs names the regions taken from theirs, so the operator can be told
// what else goes live with their change.
func Rebase(base, ours, theirs json.RawMessage) (merged json.RawMessage, fromTheirs, conflicts []string, err error) {
	var b, o, t map[string]any
	for _, x := range []struct {
		raw json.RawMessage
		dst *map[string]any
	}{{base, &b}, {ours, &o}, {theirs, &t}} {
		if err := json.Unmarshal(x.raw, x.dst); err != nil {
			return nil, nil, nil, err
		}
	}

	out := cloneMap(o)
	for _, key := range regionKeys(b, o, t) {
		if key == "routing" {
			continue
		}
		took, conflict := mergeRegion(out, key, b, o, t)
		if conflict {
			conflicts = append(conflicts, key)
		} else if took {
			fromTheirs = append(fromTheirs, key)
		}
	}

	br, or, tr := sub(b, "routing"), sub(o, "routing"), sub(t, "routing")
	routing := cloneMap(or)
	for _, key := range regionKeys(br, or, tr) {
		took, conflict := mergeRegion(routing, key, br, or, tr)
		if conflict {
			conflicts = append(conflicts, "routing."+key)
		} else if took {
			fromTheirs = append(fromTheirs, "routing."+key)
		}
	}
	_, oursHas := o["routing"]
	_, theirsHas := t["routing"]
	if len(routing) > 0 || oursHas || theirsHas {
		out["routing"] = routing
	} else {
		delete(out, "routing")
	}

	// Regions are merged independently, but rules point across them: their
	// deleting outbound B and our new rule routing to B touch different
	// regions yet leave traffic aimed at nothing. Xray accepts such a config
	// and routeTest still names B, so this is the only place it can be caught.
	conflicts = append(conflicts, danglingTargets(o, out)...)
	if len(conflicts) > 0 {
		return nil, nil, conflicts, nil
	}
	merged, err = json.MarshalIndent(out, "", "  ")
	return merged, fromTheirs, nil, err
}

// danglingTargets names the regions whose removal broke a rule in merged: a
// rule aiming at an outbound or balancer ours defined and merged no longer
// does. Targets ours never defined (subscription outbounds live outside the
// template) are not this merge's doing and are left alone.
func danglingTargets(ours, merged map[string]any) []string {
	defined := func(m map[string]any) (outbounds, balancers map[string]bool) {
		outbounds, balancers = map[string]bool{}, map[string]bool{}
		for _, o := range Outbounds(m) {
			if tag, _ := o.Raw["tag"].(string); tag != "" {
				outbounds[tag] = true
			}
		}
		for _, tag := range BalancerTags(m) {
			balancers[tag] = true
		}
		return outbounds, balancers
	}
	oursOut, oursBal := defined(ours)
	mergedOut, mergedBal := defined(merged)
	rules, err := Rules(merged)
	if err != nil {
		return nil
	}
	var lostOutbound, lostBalancer bool
	for _, r := range rules {
		if tag, _ := r.Raw["outboundTag"].(string); tag != "" && oursOut[tag] && !mergedOut[tag] {
			lostOutbound = true
		}
		if tag, _ := r.Raw["balancerTag"].(string); tag != "" && oursBal[tag] && !mergedBal[tag] {
			lostBalancer = true
		}
	}
	var out []string
	if lostOutbound {
		out = append(out, "outbounds")
	}
	if lostBalancer {
		out = append(out, "routing.balancers")
	}
	return out
}

// RebasedRules reports whether a rebase took routing.rules from theirs. The
// draft's rules then equal its new base, so rule origins are the identity.
func RebasedRules(fromTheirs []string) bool {
	for _, r := range fromTheirs {
		if r == "routing.rules" {
			return true
		}
	}
	return false
}

// mergeRegion settles one key into out (a copy of ours). took reports that
// theirs supplied the value.
func mergeRegion(out map[string]any, key string, b, o, t map[string]any) (took, conflict bool) {
	bv, bok := b[key]
	ov, ook := o[key]
	tv, tok := t[key]
	same := func(x any, xok bool, y any, yok bool) bool { return xok == yok && reflect.DeepEqual(x, y) }
	switch {
	case same(ov, ook, bv, bok):
		if same(tv, tok, bv, bok) {
			return false, false
		}
		if tok {
			out[key] = tv
		} else {
			delete(out, key)
		}
		return true, false
	case same(tv, tok, bv, bok), same(ov, ook, tv, tok):
		return false, false
	default:
		return false, true
	}
}

func regionKeys(maps ...map[string]any) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sub(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
