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
	if routing, ok := tpl["routing"].(map[string]any); ok {
		if balancers, ok := routing["balancers"].([]any); ok {
			for _, b := range balancers {
				if m, ok := b.(map[string]any); ok {
					if tag, _ := m["tag"].(string); tag != "" {
						tags = append(tags, tag)
					}
				}
			}
		}
	}
	return tags
}

// RawJSON pretty-prints any section for the detail views.
func RawJSON(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}
