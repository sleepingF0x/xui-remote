package xraycfg

import "encoding/json"

type Outbound struct {
	Index    int
	Tag      string
	Protocol string
	Raw      map[string]any
}

func Outbounds(tpl map[string]any) []Outbound {
	raw, ok := tpl["outbounds"].([]any)
	if !ok {
		return nil
	}
	out := make([]Outbound, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := m["tag"].(string)
		proto, _ := m["protocol"].(string)
		out = append(out, Outbound{Index: i, Tag: tag, Protocol: proto, Raw: m})
	}
	return out
}

// Targets lists every tag a routing rule may point at: the template's own
// outbounds plus any balancer. Subscription-derived outbounds are merged by the
// panel at config-generation time and so are not visible here.
func Targets(tpl map[string]any) []string {
	var tags []string
	for _, ob := range Outbounds(tpl) {
		if ob.Tag != "" {
			tags = append(tags, ob.Tag)
		}
	}
	return append(tags, BalancerTags(tpl)...)
}

// RawJSON pretty-prints any section for the detail views.
func RawJSON(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}

type Target struct {
	Tag      string
	Balancer bool
}

func (t Target) Label() string {
	if t.Balancer {
		return t.Tag + "（均衡器）"
	}
	return t.Tag
}
func TargetChoices(tpl map[string]any) []Target {
	var out []Target
	for _, ob := range Outbounds(tpl) {
		if ob.Tag != "" {
			out = append(out, Target{Tag: ob.Tag})
		}
	}
	for _, tag := range BalancerTags(tpl) {
		out = append(out, Target{Tag: tag, Balancer: true})
	}
	return out
}
func (t Target) Apply(r Rule) { r.SetTarget(t.Tag, t.Balancer) }

func (t Target) Matches(r Rule) bool {
	if t.Balancer {
		return r.Raw["balancerTag"] == t.Tag && r.Raw["outboundTag"] == nil
	}
	return r.Raw["outboundTag"] == t.Tag && r.Raw["balancerTag"] == nil
}
