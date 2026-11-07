package verify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sleepingF0x/xui-remote/internal/state"
	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
)

// Plan derives probes from the applied final state, never from edit history.
func Plan(base, draft json.RawMessage, origins ...[]int) ([]state.Check, error) {
	changes, err := xraycfg.Changes(base, draft, origins...)
	if err != nil {
		return nil, err
	}
	var out []state.Check
	for _, change := range changes {
		if change.Kind != "规则" || change.After == nil {
			continue
		}
		checks := ruleChecks(xraycfg.Rule{Index: change.AfterIndex, Raw: change.After})
		out = append(out, checks[:min(3, len(checks))]...)
	}
	return out, nil
}

// ruleChecks is one probe per matcher of r, each carrying the rule's other
// constraints. A probe that cannot be built faithfully says why in Skip.
func ruleChecks(r xraycfg.Rule) []state.Check {
	field, values := r.MatchValues()
	ch := state.Check{Kind: field, Index: r.Index, Port: 443, Network: "tcp"}
	ch.Expect, _ = r.Raw["outboundTag"].(string)
	if target, ok := r.Raw["balancerTag"].(string); ok && target != "" {
		ch.Expect = target
		ch.Balancer = true
	}
	ch.User = first(r.Raw["user"])
	ch.Inbound = first(r.Raw["inboundTag"])
	ch.Protocol = first(r.Raw["protocol"])
	if network, ok := r.Raw["network"].(string); ok && network != "" {
		ch.Network = strings.TrimSpace(strings.Split(network, ",")[0])
	}
	if port, exists := r.Raw["port"]; exists {
		text := strings.Split(fmt.Sprint(port), ",")[0]
		text = strings.Split(text, "-")[0]
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 1 || n > 65535 {
			ch.Skip = "目标端口无法取样"
		} else {
			ch.Port = n
		}
	}
	supported := map[string]bool{"type": true, "ruleTag": true, "domain": true, "ip": true, "user": true, "inboundTag": true, "protocol": true, "network": true, "port": true, "outboundTag": true, "balancerTag": true}
	var unsupported []string
	for k, v := range r.Raw {
		if !supported[k] && v != nil {
			unsupported = append(unsupported, k)
		}
	}
	sort.Strings(unsupported)
	if len(unsupported) > 0 {
		ch.Skip = "测试接口无法模拟约束: " + strings.Join(unsupported, ", ")
	}
	if r.Raw["domain"] != nil && r.Raw["ip"] != nil {
		ch.Skip = "同时约束域名和 IP，无法可靠取样"
	}
	if len(values) == 0 {
		values = []string{r.Summary()}
		ch.Skip = "没有可取样的域名或 IP"
	}
	if ch.Network != "tcp" && ch.Network != "udp" {
		ch.Skip = "网络类型无法取样"
	}
	out := make([]state.Check, 0, len(values))
	for _, value := range values {
		sample := ch
		sample.Value = value
		out = append(out, sample)
	}
	return out
}

func first(v any) string {
	if values, ok := v.([]any); ok && len(values) > 0 {
		s, _ := values[0].(string)
		return s
	}
	s, _ := v.(string)
	return s
}
