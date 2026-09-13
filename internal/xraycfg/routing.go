// Package xraycfg reads and edits sections of the Xray config template that the
// panel stores as one JSON document.
package xraycfg

import (
	"fmt"
	"strings"
)

// Rule is a routing rule in the order it appears; Index is its position, which
// decides everything because the router takes the first match.
type Rule struct {
	Index int
	Raw   map[string]any
}

// Rules returns routing.rules, or an empty slice when the template has no
// routing section yet.
func Rules(tpl map[string]any) ([]Rule, error) {
	routing, ok := tpl["routing"].(map[string]any)
	if !ok {
		return nil, nil
	}
	raw, ok := routing["rules"].([]any)
	if !ok {
		return nil, nil
	}
	out := make([]Rule, 0, len(raw))
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("routing.rules[%d] 不是对象", i)
		}
		out = append(out, Rule{Index: i, Raw: m})
	}
	return out, nil
}

// SetRules writes the slice back, creating the routing section if needed.
func SetRules(tpl map[string]any, rules []Rule) {
	routing, ok := tpl["routing"].(map[string]any)
	if !ok {
		routing = map[string]any{}
		tpl["routing"] = routing
	}
	raw := make([]any, len(rules))
	for i, r := range rules {
		raw[i] = r.Raw
	}
	routing["rules"] = raw
}

// Target is the outbound or balancer the rule sends matching traffic to.
func (r Rule) Target() string {
	if s, _ := r.Raw["outboundTag"].(string); s != "" {
		return s
	}
	if s, _ := r.Raw["balancerTag"].(string); s != "" {
		return s + " (balancer)"
	}
	return "(未设置)"
}

// Summary is a one-line description for the rule list.
func (r Rule) Summary() string {
	var parts []string
	if v := stringsOf(r.Raw["domain"]); len(v) > 0 {
		parts = append(parts, "domain: "+joinPreview(v, 2))
	}
	if v := stringsOf(r.Raw["ip"]); len(v) > 0 {
		parts = append(parts, "ip: "+joinPreview(v, 2))
	}
	if v := scalarString(r.Raw["port"]); v != "" {
		parts = append(parts, "port: "+v)
	}
	if v := scalarString(r.Raw["network"]); v != "" {
		parts = append(parts, "net: "+v)
	}
	if v := stringsOf(r.Raw["inboundTag"]); len(v) > 0 {
		parts = append(parts, "in: "+joinPreview(v, 2))
	}
	if v := stringsOf(r.Raw["protocol"]); len(v) > 0 {
		parts = append(parts, "proto: "+joinPreview(v, 2))
	}
	if v := stringsOf(r.Raw["user"]); len(v) > 0 {
		parts = append(parts, "user: "+joinPreview(v, 2))
	}
	if len(parts) == 0 {
		return "(无匹配条件)"
	}
	return strings.Join(parts, "  ")
}

// IsCatchAll reports whether the rule constrains nothing that narrows traffic,
// so every rule after it is unreachable for the traffic class it covers.
// inboundTag counts as narrowing: a rule scoped to one inbound never sees
// traffic arriving on another, so it shadows nothing for the rest of them.
func (r Rule) IsCatchAll() bool {
	for _, key := range []string{"domain", "ip", "port", "source", "sourcePort", "protocol", "user", "attrs", "inboundTag"} {
		if has(r.Raw[key]) {
			return false
		}
	}
	return true
}

func (r Rule) Tag() string {
	s, _ := r.Raw["ruleTag"].(string)
	return s
}

// Insert places a rule at position idx, clamped into range.
func Insert(rules []Rule, idx int, rule Rule) []Rule {
	if idx < 0 {
		idx = 0
	}
	if idx > len(rules) {
		idx = len(rules)
	}
	out := make([]Rule, 0, len(rules)+1)
	out = append(out, rules[:idx]...)
	out = append(out, rule)
	out = append(out, rules[idx:]...)
	return reindex(out)
}

func Remove(rules []Rule, idx int) ([]Rule, error) {
	if idx < 0 || idx >= len(rules) {
		return nil, fmt.Errorf("规则序号 %d 不存在", idx+1)
	}
	out := append(append([]Rule{}, rules[:idx]...), rules[idx+1:]...)
	return reindex(out), nil
}

func Move(rules []Rule, from, to int) ([]Rule, error) {
	if from < 0 || from >= len(rules) {
		return nil, fmt.Errorf("规则序号 %d 不存在", from+1)
	}
	if to < 0 {
		to = 0
	}
	if to >= len(rules) {
		to = len(rules) - 1
	}
	rule := rules[from]
	out, err := Remove(rules, from)
	if err != nil {
		return nil, err
	}
	return Insert(out, to, rule), nil
}

// SafeFloor is the earliest index a new destination rule may take. A stock
// 3x-ui rule set opens with infrastructure rules that carry no domain matcher
// — the api inbound's route, the private-address block, protocol blocks — and
// their order is load-bearing: the api inbound's own traffic targets 127.0.0.1,
// so a rule placed above the api route can swallow it and take the panel's
// gRPC API (stats, online clients, route testing) down with it.
func SafeFloor(rules []Rule) int {
	for i, r := range rules {
		if has(r.Raw["domain"]) {
			return i
		}
	}
	return len(rules)
}

// SuggestPosition returns the index a new rule should take: just before the
// first catch-all, but never above SafeFloor. It is a heuristic — overlap
// between geosite categories is not evaluated.
func SuggestPosition(rules []Rule) int {
	floor := SafeFloor(rules)
	for i, r := range rules {
		if r.IsCatchAll() && i >= floor {
			return i
		}
	}
	if floor > len(rules) {
		return len(rules)
	}
	return len(rules)
}

// ClampPosition keeps a chosen index out of the infrastructure prefix.
func ClampPosition(rules []Rule, idx int) int {
	if floor := SafeFloor(rules); idx < floor {
		return floor
	}
	if idx > len(rules) {
		return len(rules)
	}
	return idx
}

func reindex(rules []Rule) []Rule {
	for i := range rules {
		rules[i].Index = i
	}
	return rules
}

func has(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case []any:
		return len(t) > 0
	case string:
		return t != ""
	default:
		return true
	}
}

func stringsOf(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%d", int64(t))
	default:
		return ""
	}
}

func joinPreview(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s … +%d", strings.Join(items[:max], ", "), len(items)-max)
}
