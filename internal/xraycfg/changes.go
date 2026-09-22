package xraycfg

import (
	"encoding/json"
	"reflect"
)

// Change identifies an object in both the baseline and final template.
// A missing side has index -1. Moved means relative order changed, not that an
// insertion merely shifted the displayed number.
type Change struct {
	Kind                    string
	BeforeIndex, AfterIndex int
	Before, After           map[string]any
	Moved                   bool
}

func Changes(base, draft json.RawMessage, ruleOrigins ...[]int) ([]Change, error) {
	var a, b map[string]any
	if err := json.Unmarshal(base, &a); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(draft, &b); err != nil {
		return nil, err
	}
	if reflect.DeepEqual(a, b) {
		return nil, nil
	}
	ar, err := Rules(a)
	if err != nil {
		return nil, err
	}
	br, err := Rules(b)
	if err != nil {
		return nil, err
	}
	aa, bb := make([]map[string]any, len(ar)), make([]map[string]any, len(br))
	for i, r := range ar {
		aa[i] = r.Raw
	}
	for i, r := range br {
		bb[i] = r.Raw
	}
	origins := matchObjects(aa, bb, ruleRelated)
	if len(ruleOrigins) > 0 && validOrigins(ruleOrigins[0], len(aa), len(bb)) {
		origins = ruleOrigins[0]
	}
	out := objectChanges("规则", aa, bb, origins)
	ao, bo := Outbounds(a), Outbounds(b)
	aa, bb = make([]map[string]any, len(ao)), make([]map[string]any, len(bo))
	for i, o := range ao {
		aa[i] = o.Raw
	}
	for i, o := range bo {
		bb[i] = o.Raw
	}
	out = append(out, objectChanges("出站", aa, bb, matchObjects(aa, bb, func(a, b map[string]any) bool { return a["tag"] != "" && a["tag"] != nil && a["tag"] == b["tag"] }))...)
	// Preserve visibility of older drafts containing edits outside these lists.
	for _, t := range []map[string]any{a, b} {
		delete(t, "outbounds")
		if routing, ok := t["routing"].(map[string]any); ok {
			delete(routing, "rules")
			if len(routing) == 0 {
				delete(t, "routing")
			}
		}
	}
	if !reflect.DeepEqual(a, b) {
		out = append(out, Change{Kind: "其他配置", Before: a, After: b})
	}
	return out, nil
}

func matchObjects(a, b []map[string]any, related func(map[string]any, map[string]any) bool) []int {
	matched := make([]int, len(b))
	used := make([]bool, len(a))
	for j := range matched {
		matched[j] = -1
	}
	// Exact objects first, so a neighbouring deletion cannot steal an identity.
	for j, y := range b {
		for i, x := range a {
			if !used[i] && reflect.DeepEqual(x, y) {
				matched[j] = i
				used[i] = true
				break
			}
		}
	}
	for j, y := range b {
		if matched[j] >= 0 {
			continue
		}
		candidate := -1
		for i, x := range a {
			if !used[i] && related(x, y) {
				if candidate >= 0 {
					candidate = -1
					break
				}
				candidate = i
			}
		}
		if candidate >= 0 {
			matched[j] = candidate
			used[candidate] = true
		}
	}
	return matched
}
func objectChanges(kind string, a, b []map[string]any, matched []int) []Change {
	used := make([]bool, len(a))
	for _, i := range matched {
		if i >= 0 {
			used[i] = true
		}
	}
	// Include both sides of an inversion: either can shadow the other now.
	// Insertions alone preserve relative order and do not mark neighbours.
	moved := make([]bool, len(b))
	for j, i := range matched {
		if i < 0 {
			continue
		}
		for k := 0; k < j; k++ {
			if matched[k] > i {
				moved[j] = true
				moved[k] = true
			}
		}
	}
	var out []Change
	for i, x := range a {
		if !used[i] {
			out = append(out, Change{Kind: kind, BeforeIndex: i, AfterIndex: -1, Before: x})
		}
	}
	for j, y := range b {
		i := matched[j]
		if i < 0 {
			out = append(out, Change{Kind: kind, BeforeIndex: -1, AfterIndex: j, After: y})
			continue
		}
		if !reflect.DeepEqual(a[i], y) || moved[j] {
			out = append(out, Change{Kind: kind, BeforeIndex: i, AfterIndex: j, Before: a[i], After: y, Moved: moved[j]})
		}
	}
	return out
}

func ruleRelated(a, b map[string]any) bool {
	if tag, ok := a["ruleTag"].(string); ok && tag != "" {
		return tag == b["ruleTag"]
	}
	conditions := func(r map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range r {
			if k != "outboundTag" && k != "balancerTag" {
				out[k] = v
			}
		}
		return out
	}
	if reflect.DeepEqual(conditions(a), conditions(b)) {
		return true
	}
	// Match value edits by unchanged target and at least one surviving matcher.
	if a["outboundTag"] != b["outboundTag"] || a["balancerTag"] != b["balancerTag"] {
		return false
	}
	for _, field := range []string{"domain", "ip"} {
		x, _ := a[field].([]any)
		y, _ := b[field].([]any)
		for _, v := range x {
			for _, w := range y {
				if reflect.DeepEqual(v, w) {
					return true
				}
			}
		}
	}
	return false
}

// TrackRuleOrigins carries baseline identity through individual edits. It is
// staging metadata only; the panel template and snapshots remain unchanged.
// Older drafts without metadata start from content matching.
func TrackRuleOrigins(base, before, after json.RawMessage, origins []int) ([]int, error) {
	read := func(raw json.RawMessage) ([]map[string]any, error) {
		var tpl map[string]any
		if err := json.Unmarshal(raw, &tpl); err != nil {
			return nil, err
		}
		rules, err := Rules(tpl)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, len(rules))
		for i, r := range rules {
			out[i] = r.Raw
		}
		return out, nil
	}
	baseline, err := read(base)
	if err != nil {
		return nil, err
	}
	previous, err := read(before)
	if err != nil {
		return nil, err
	}
	next, err := read(after)
	if err != nil {
		return nil, err
	}
	if !validOrigins(origins, len(baseline), len(previous)) {
		origins = matchObjects(baseline, previous, ruleRelated)
	}
	// Module edits change one object at a time. Preserve position identity
	// for field edits, including adjacent identical rules; exact matching
	// would otherwise steal the unchanged duplicate's identity.
	if len(previous) == len(next) {
		changed := 0
		for i := range previous {
			if !reflect.DeepEqual(previous[i], next[i]) {
				changed++
			}
		}
		if changed <= 1 {
			return append([]int{}, origins...), nil
		}
	}
	mapping := matchObjects(previous, next, ruleRelated)
	for j, i := range mapping {
		if i >= 0 {
			mapping[j] = origins[i]
		}
	}
	return mapping, nil
}
func validOrigins(origins []int, baseLen, draftLen int) bool {
	if origins == nil || len(origins) != draftLen {
		return false
	}
	seen := map[int]bool{}
	for _, i := range origins {
		if i < -1 || i >= baseLen || (i >= 0 && seen[i]) {
			return false
		}
		seen[i] = true
	}
	return true
}
