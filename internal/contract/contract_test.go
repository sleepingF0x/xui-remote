// Package contract_test drives the bot's panel-facing paths against a real,
// disposable 3x-ui. The integration tests mock the panel, so they can only
// confirm the bot against what its author believed the panel does; this is
// where that belief is checked. CI starts a pinned panel in Docker (see
// .github/workflows/ci.yml); locally, point it at a throwaway panel of your own:
//
//	XUI_CONTRACT_BASE_URL=http://127.0.0.1:2053/panel/api/ \
//	XUI_CONTRACT_TOKEN=<admin token> go test -count=1 -v ./internal/contract/
//
// It rewrites the Xray config, so it refuses a panel with any inbound it did
// not create itself.
package contract_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/selftest"
	"github.com/sleepingF0x/xui-remote/internal/state"
	"github.com/sleepingF0x/xui-remote/internal/txn"
	"github.com/sleepingF0x/xui-remote/internal/verify"
	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
)

const (
	probeDomain = "contract.example"
	// broadMatcher also matches probeDomain, so a rule using it placed above
	// the probe rule takes that traffic over.
	broadMatcher = "domain:example"
	// contractRemark marks the one inbound this test creates; any other
	// inbound means the panel is in use and must not be touched.
	contractRemark = "xui-remote-contract"
	// publicHost exercises panel.public_host: without it, share links built
	// for a loopback dial would point clients at 127.0.0.1.
	publicHost = "panel.contract.example"
)

func TestPanelContract(t *testing.T) {
	base, token := os.Getenv("XUI_CONTRACT_BASE_URL"), os.Getenv("XUI_CONTRACT_TOKEN")
	if base == "" || token == "" {
		t.Skip("set XUI_CONTRACT_BASE_URL and XUI_CONTRACT_TOKEN to run against a disposable panel")
	}
	p := panel.New(panel.Options{BaseURL: base, Token: token, PublicHost: publicHost, Timeout: 30 * time.Second})

	inbounds, err := p.ListInbounds()
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	ours := false
	for _, in := range inbounds {
		if in.Remark != contractRemark {
			t.Fatalf("refusing to run: inbound %q is not this test's. It rewrites the Xray config; point it at a throwaway panel", in.Remark)
		}
		ours = true
	}
	if !ours {
		// The self-test reads inbounds, clients and share links; an empty
		// panel would leave those decoders unexercised.
		addInbound(t, p)
	}
	waitRunning(t, p)

	orig, err := p.GetXraySetting()
	if err != nil {
		t.Fatalf("read xray setting: %v", err)
	}
	t.Cleanup(func() {
		if err := p.UpdateXraySetting(orig); err != nil {
			t.Logf("restoring the original template: %v", err)
		}
	})
	tags := outboundTags(t, orig.Template)
	if len(tags) < 2 {
		t.Fatalf("need two outbounds in the default template, have %v", tags)
	}
	target, other := tags[1], tags[0]

	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"read paths match what the bot decodes", func(t *testing.T) {
			var out bytes.Buffer
			err := selftest.New(p, &out).Run()
			t.Log("\n" + out.String())
			if err != nil {
				t.Fatal(err)
			}
		}},

		{"an apply reaches the running core before it returns", func(t *testing.T) {
			m := newManager(t, p)
			stage(t, m, func(rules []xraycfg.Rule) []xraycfg.Rule {
				return xraycfg.Insert(rules, len(rules), rule("domain:"+probeDomain, target))
			})
			pending, _ := m.Pending()
			res, err := m.Apply(false)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Health.Running {
				t.Fatalf("core not running after apply: %+v", res.Health)
			}
			// Verification probes straight after the apply returns; that is
			// only meaningful if the panel applied the config synchronously.
			got, err := p.RouteTest(panel.RouteTestRequest{Domain: probeDomain, Port: 443, Network: "tcp"})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Matched || got.OutboundTag != target {
				t.Fatalf("routeTest right after apply: %+v, want %s", got, target)
			}
			checks, err := verify.Plan(pending.Base, pending.Draft, pending.RuleOrigins)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range verify.New(p).Run(checks) {
				if !r.Verified() || !r.OK {
					t.Fatalf("post-apply verification: %+v", r)
				}
			}
		}},

		{"a rule placed above another is reported as rerouting it", func(t *testing.T) {
			m := newManager(t, p)
			stage(t, m, func(rules []xraycfg.Rule) []xraycfg.Rule {
				return xraycfg.Insert(rules, len(rules)-1, rule(broadMatcher, other))
			})
			pending, _ := m.Pending()
			v := verify.New(p)
			baseline, err := v.Baseline(pending.Base, pending.Draft, pending.RuleOrigins)
			if err != nil {
				t.Fatal(err)
			}
			if baseline.Size() == 0 {
				t.Fatal("no untouched rule below the edit was probed")
			}
			if _, err := m.Apply(false); err != nil {
				t.Fatal(err)
			}
			drifts, checked := v.Recheck(baseline)
			t.Logf("rechecked %d, drifts %+v", checked, drifts)
			for _, d := range drifts {
				if d.Target == probeDomain && d.After == other {
					return
				}
			}
			t.Fatalf("%s moved to %s but no drift was reported", probeDomain, other)
		}},

		{"a config the core cannot run is undone and stays staged", func(t *testing.T) {
			m := newManager(t, p)
			before := live(t, p)
			if err := m.Edit("contract", "broken outbound", func(tpl map[string]any) error {
				outbounds, _ := tpl["outbounds"].([]any)
				tpl["outbounds"] = append(outbounds, map[string]any{"tag": "contract-broken", "protocol": "contract-no-such-protocol"})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_, err := m.Apply(false)
			if err == nil {
				t.Fatal("the panel accepted an outbound with an unknown protocol")
			}
			var failed *txn.ApplyError
			if errors.As(err, &failed) {
				t.Logf("panel stored the draft, then the core refused it; restored=%v health=%+v", failed.Restored, failed.Health)
			} else {
				t.Logf("panel refused the draft before storing it: %v", err)
			}
			// Whichever way the panel refuses, the operator must be left with
			// the previous config live, the core up and the draft kept.
			if !sameJSON(live(t, p), before) {
				t.Fatal("the panel was left holding the broken draft")
			}
			waitRunning(t, p)
			if _, err := m.Pending(); err != nil {
				t.Fatalf("the draft was lost: %v", err)
			}
		}},

		{"rollback restores the snapshot and keeps the core up", func(t *testing.T) {
			m := newManager(t, p)
			stage(t, m, func(rules []xraycfg.Rule) []xraycfg.Rule {
				return xraycfg.Insert(rules, len(rules), rule("domain:rollback."+probeDomain, other))
			})
			before := live(t, p)
			res, err := m.Apply(false)
			if err != nil {
				t.Fatal(err)
			}
			health, err := m.Rollback(res.Snapshot.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !health.Running {
				t.Fatalf("core not running after rollback: %+v", health)
			}
			if !sameJSON(live(t, p), before) {
				t.Fatal("rollback did not restore the snapshot")
			}
		}},

		{"an unkept change is rolled back when its window closes", func(t *testing.T) {
			m := newManager(t, p, txn.WithConfirmWindow(time.Minute))
			stage(t, m, func(rules []xraycfg.Rule) []xraycfg.Rule {
				return xraycfg.Insert(rules, len(rules), rule("domain:expiry."+probeDomain, other))
			})
			before := live(t, p)
			res, err := m.Apply(false)
			if err != nil {
				t.Fatal(err)
			}
			if res.ConfirmBy.IsZero() {
				t.Fatal("no confirmation window armed")
			}
			out, err := m.ExpireDue(time.Now().Add(2 * time.Minute))
			if err != nil || out == nil || !out.RolledBack {
				t.Fatalf("expiry: %+v, %v", out, err)
			}
			if !out.Health.Running {
				t.Fatalf("core not running after the automatic rollback: %+v", out.Health)
			}
			if !sameJSON(live(t, p), before) {
				t.Fatal("the expired change is still live")
			}
		}},
	}
	for _, s := range steps {
		// Each step builds on the panel state the previous one left.
		if !t.Run(s.name, s.fn) {
			return
		}
	}
}

func addInbound(t *testing.T, p *panel.Client) {
	t.Helper()
	settings, _ := json.Marshal(map[string]any{
		"clients": []any{map[string]any{
			"id": "6f1c8a52-3b8e-4d0a-9c1e-2a7d5b4e9f01", "email": "contract@xui-remote.test",
			"enable": true, "subId": "xuiremotecontract", "flow": "",
		}},
		"decryption": "none",
	})
	form := url.Values{}
	form.Set("remark", contractRemark)
	form.Set("enable", "true")
	form.Set("port", "24443")
	form.Set("protocol", "vless")
	form.Set("tag", "contract-in-24443")
	form.Set("settings", string(settings))
	form.Set("streamSettings", `{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"none"}}}`)
	form.Set("sniffing", `{"enabled":false}`)
	if _, err := p.Post("inbounds/add", form); err != nil {
		t.Fatalf("create the contract inbound: %v", err)
	}
}

func newManager(t *testing.T, p *panel.Client, opts ...txn.Option) *txn.Manager {
	t.Helper()
	store, err := state.NewStore(t.TempDir(), 20)
	if err != nil {
		t.Fatal(err)
	}
	return txn.New(p, store, opts...)
}

func stage(t *testing.T, m *txn.Manager, edit func([]xraycfg.Rule) []xraycfg.Rule) {
	t.Helper()
	if err := m.Edit("contract", "contract edit", func(tpl map[string]any) error {
		rules, err := xraycfg.Rules(tpl)
		if err != nil {
			return err
		}
		xraycfg.SetRules(tpl, edit(rules))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func rule(matcher, outbound string) xraycfg.Rule {
	return xraycfg.Rule{Raw: map[string]any{"type": "field", "domain": []any{matcher}, "outboundTag": outbound}}
}

func live(t *testing.T, p *panel.Client) json.RawMessage {
	t.Helper()
	s, err := p.GetXraySetting()
	if err != nil {
		t.Fatal(err)
	}
	return s.Template
}

func outboundTags(t *testing.T, template json.RawMessage) []string {
	t.Helper()
	var tpl struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(template, &tpl); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, o := range tpl.Outbounds {
		if o.Tag != "" {
			tags = append(tags, o.Tag)
		}
	}
	return tags
}

// waitRunning gives a freshly started panel time to bring its core up.
func waitRunning(t *testing.T, p *panel.Client) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		s, err := p.Status()
		if err == nil && s.Xray.State == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("xray never reached running: status %+v, err %v", s, err)
		}
		time.Sleep(time.Second)
	}
}

func sameJSON(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
