package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
)

// maxCollateral bounds how many untouched rules are re-probed. Each probe is
// one local gRPC call, but a long rule list should not stall the apply.
const maxCollateral = 20

// Baseline is how the running core routed traffic of untouched rules before an
// apply. Those rules are identical in both configs, so where their traffic went
// is a ground truth the change was never meant to move: probing the same
// targets afterwards exposes rerouting that verifying the edited rules misses,
// such as a new broad rule above them swallowing their traffic.
type Baseline struct {
	probes []probe
}

type probe struct {
	// index is the rule's position in the applied config.
	index    int
	label    string
	source   string
	target   string
	balancer bool
	req      panel.RouteTestRequest
	before   route
}

// route is where the core sent one probe.
type route struct {
	matched  bool
	outbound string
	groups   []string
}

func routeOf(r *panel.RouteTestResult) route {
	return route{matched: r.Matched, outbound: r.OutboundTag, groups: r.GroupTags}
}

// key is the identity compared before and after. Through a balancer only the
// path counts: the node it picks may legitimately differ between two probes.
func (r route) key() string {
	if len(r.groups) > 0 {
		return "via:" + strings.Join(r.groups, ">")
	}
	if !r.matched {
		return "unmatched:" + r.outbound
	}
	return r.outbound
}

func (r route) String() string {
	switch {
	case len(r.groups) > 0:
		return strings.Join(r.groups, " → ") + " → " + r.outbound
	case !r.matched:
		return "(未匹配任何规则)"
	default:
		return r.outbound
	}
}

// Size is how many probes the baseline holds.
func (b *Baseline) Size() int {
	if b == nil {
		return 0
	}
	return len(b.probes)
}

// Drift is an untouched rule whose sampled traffic now goes somewhere else.
type Drift struct {
	Index  int
	Label  string
	Source string
	Target string
	Before string
	After  string
}

// Baseline probes the untouched rules the change could affect, against the
// core that is running now. Call it before the apply writes anything.
func (v *Verifier) Baseline(base, draft json.RawMessage, origins ...[]int) (*Baseline, error) {
	candidates, err := collateralRules(base, draft, origins...)
	if err != nil {
		return nil, err
	}
	b := &Baseline{}
	if len(candidates) == 0 {
		return b, nil
	}
	// routeTest asks the running core; a stopped one has nothing to say.
	if s, err := v.panel.Status(); err != nil || s.Xray.State != "running" {
		return b, nil
	}
	for _, r := range candidates {
		if len(b.probes) >= maxCollateral {
			break
		}
		p, ok, err := v.probeRule(r)
		if err != nil {
			// The probe call itself is failing, not this rule; the rest
			// would fail the same way.
			return b, nil
		}
		if ok {
			b.probes = append(b.probes, p)
		}
	}
	return b, nil
}

// probeRule builds and runs the first faithful probe for r. A rule none of
// whose matchers can be sampled is left out rather than reported as skipped:
// the operator never asked about it.
func (v *Verifier) probeRule(r xraycfg.Rule) (probe, bool, error) {
	checks := ruleChecks(r)
	for _, ch := range checks[:min(3, len(checks))] {
		if ch.Skip != "" {
			continue
		}
		target, _ := v.resolve(ch)
		if target == "" {
			continue
		}
		p := probe{
			index:    r.Index,
			label:    fmt.Sprintf("第 %d 条 · %s → %s", r.Index+1, r.Summary(), r.Target()),
			source:   ch.Value,
			target:   target,
			balancer: ch.Balancer,
			req:      request(ch, target),
		}
		got, err := v.panel.RouteTest(p.req)
		if err != nil {
			return probe{}, false, err
		}
		p.before = routeOf(got)
		// Without the balancer path the final node is all there is to compare,
		// and that varies by itself.
		if p.balancer && len(p.before.groups) == 0 {
			return probe{}, false, nil
		}
		return p, true, nil
	}
	return probe{}, false, nil
}

// Recheck replays the baseline against the core now running. checked counts
// the probes that could be compared; a probe that errors is not a drift.
func (v *Verifier) Recheck(b *Baseline) (drifts []Drift, checked int) {
	if b == nil {
		return nil, 0
	}
	for _, p := range b.probes {
		got, err := v.panel.RouteTest(p.req)
		if err != nil {
			continue
		}
		after := routeOf(got)
		if p.balancer && len(after.groups) == 0 {
			continue
		}
		checked++
		if after.key() != p.before.key() {
			drifts = append(drifts, Drift{Index: p.index, Label: p.label, Source: p.source, Target: p.target,
				Before: p.before.String(), After: after.String()})
		}
	}
	return drifts, checked
}

// collateralRules lists the applied config's untouched rules that the change
// could reroute, nearest the change first. Routing is first-match, so a rule
// above every edited rule keeps its traffic unless an outbound or another
// routing setting changed too.
func collateralRules(base, draft json.RawMessage, origins ...[]int) ([]xraycfg.Rule, error) {
	changes, err := xraycfg.Changes(base, draft, origins...)
	if err != nil {
		return nil, err
	}
	tpl, err := xraycfg.ParseTemplate(draft)
	if err != nil {
		return nil, err
	}
	rules, err := xraycfg.Rules(tpl)
	if err != nil {
		return nil, err
	}

	edited := map[int]bool{}
	first := len(rules)
	deleted := 0
	other := false
	for _, ch := range changes {
		if ch.Kind != "规则" {
			other = true
			continue
		}
		if ch.After == nil {
			deleted++
		}
	}
	for _, ch := range changes {
		switch {
		case ch.Kind != "规则":
		case ch.After != nil:
			edited[ch.AfterIndex] = true
			first = min(first, ch.AfterIndex)
		default:
			// A deleted rule's place in the final list is only known
			// approximately; shifting it up by every deletion errs towards
			// probing one rule too many rather than one too few.
			first = min(first, max(0, ch.BeforeIndex-deleted))
		}
	}

	var below, above []xraycfg.Rule
	for _, r := range rules {
		switch {
		case edited[r.Index], r.Disabled():
			// A disabled rule routes nothing, so it has no traffic to lose.
		case r.Index >= first:
			below = append(below, r)
		case other:
			above = append(above, r)
		}
	}
	sort.SliceStable(above, func(i, j int) bool { return above[i].Index > above[j].Index })
	return slices.Concat(below, above), nil
}
