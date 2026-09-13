// Package selftest exercises every read path against a real panel. Unit tests
// cannot catch a panel version serializing a field differently than expected,
// which is exactly the failure mode that shows up first in production.
package selftest

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/state"
	"github.com/sleepingF0x/xui-remote/internal/verify"
	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
)

// secretish masks anything that looks like a credential inside a share link,
// so a self-test transcript can be pasted somewhere without leaking accounts.
var secretish = regexp.MustCompile(`(?i)(://)[^@/\s]+(@)`)

type Runner struct {
	panel *panel.Client
	out   io.Writer
	pass  int
	fail  int
}

func New(p *panel.Client, out io.Writer) *Runner { return &Runner{panel: p, out: out} }

func (r *Runner) step(name string, fn func() (string, error)) {
	detail, err := fn()
	if err != nil {
		r.fail++
		fmt.Fprintf(r.out, "  ✗ %-26s %v\n", name, err)
		return
	}
	r.pass++
	fmt.Fprintf(r.out, "  ✓ %-26s %s\n", name, detail)
}

// Run performs read-only checks only; nothing here writes to the panel.
func (r *Runner) Run() error {
	fmt.Fprintln(r.out, "面板")
	r.step("server/status", func() (string, error) {
		s, err := r.panel.Status()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("v%s · Xray %s %s", s.PanelVersion, s.Xray.Version, s.Xray.State), nil
	})
	r.step("setting/defaultSettings", func() (string, error) {
		s, err := r.panel.PanelSettings()
		if err != nil {
			return "", err
		}
		if s.SubEnable && s.SubURI == "" {
			return "", fmt.Errorf("订阅已启用但 subURI 为空，生成的订阅链接客户端访问不到")
		}
		return fmt.Sprintf("订阅启用=%v", s.SubEnable), nil
	})

	tpl, tplErr := r.configTemplate()
	fmt.Fprintln(r.out, "\n配置模板")
	r.step("xray/ 读取与解析", func() (string, error) {
		if tplErr != nil {
			return "", tplErr
		}
		return fmt.Sprintf("%d 个顶层字段", len(tpl)), nil
	})

	var rules []xraycfg.Rule
	var targets []string
	if tplErr == nil {
		r.step("routing.rules 解析", func() (string, error) {
			var err error
			rules, err = xraycfg.Rules(tpl)
			if err != nil {
				return "", err
			}
			floor := xraycfg.SafeFloor(rules)
			return fmt.Sprintf("%d 条 · 基础设施前缀 %d 条 · 推荐插入位 %d",
				len(rules), floor, xraycfg.SuggestPosition(rules)+1), nil
		})
		r.step("outbounds 解析", func() (string, error) {
			obs := xraycfg.Outbounds(tpl)
			targets = xraycfg.Targets(tpl)
			if len(obs) == 0 {
				return "", fmt.Errorf("没有解析出任何出站")
			}
			return fmt.Sprintf("%d 个出站 · %d 个可用目标 · 默认 %s",
				len(obs), len(targets), obs[0].Tag), nil
		})
		r.step("规则目标完整性", func() (string, error) {
			known := map[string]bool{}
			for _, t := range targets {
				known[t] = true
			}
			var dangling []string
			for _, rule := range rules {
				if tag, _ := rule.Raw["outboundTag"].(string); tag != "" && !known[tag] {
					dangling = append(dangling, fmt.Sprintf("第%d条→%s", rule.Index+1, tag))
				}
			}
			if len(dangling) > 0 {
				// Subscription outbounds are merged by the panel at generation
				// time, so a tag missing here is not necessarily broken.
				return fmt.Sprintf("%d 条指向模板外的出站（可能来自订阅）: %s",
					len(dangling), strings.Join(dangling, ", ")), nil
			}
			return "全部指向已知出站", nil
		})
	}

	fmt.Fprintln(r.out, "\n路由测试")
	r.step("routeTest", func() (string, error) {
		res, err := r.panel.RouteTest(panel.RouteTestRequest{Domain: "example.com", Port: 443, Network: "tcp"})
		if err != nil {
			return "", err
		}
		if !res.Matched {
			return "example.com → 未匹配规则（走默认出站）", nil
		}
		return "example.com → " + res.OutboundTag, nil
	})

	fmt.Fprintln(r.out, "\n分类与验证")
	sample := firstCategory(rules)
	r.step("geodata/entries", func() (string, error) {
		if sample == "" {
			return "配置里没有引用分类，跳过", nil
		}
		return r.probeCategory(sample)
	})
	r.step("提交后验证链路", func() (string, error) {
		if sample == "" {
			return "无分类可测，跳过", nil
		}
		results := verify.New(r.panel).Run([]state.Check{{Kind: "domain", Value: sample, Expect: "__probe__"}})
		got := results[0]
		if !got.Verified() {
			return "", fmt.Errorf("取样失败: %s", got.Skipped)
		}
		return fmt.Sprintf("%s 取样 %s → %s", sample, got.Target, got.Actual), nil
	})

	fmt.Fprintln(r.out, "\n出站")
	r.step("outbound-subs 列表", func() (string, error) {
		subs, err := r.panel.ListOutboundSubs()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d 个订阅", len(subs)), nil
	})

	fmt.Fprintln(r.out, "\n入站与客户端")
	r.step("inbounds/list/slim", func() (string, error) {
		ins, err := r.panel.ListInbounds()
		if err != nil {
			return "", err
		}
		if len(ins) == 0 {
			return "", fmt.Errorf("没有解析出任何入站")
		}
		return fmt.Sprintf("%d 个入站 · 首个 %s:%d", len(ins), ins[0].Protocol, ins[0].Port), nil
	})

	var email, subID string
	r.step("clients/list/paged", func() (string, error) {
		page, err := r.panel.SearchClients("", 1, 5)
		if err != nil {
			return "", err
		}
		if len(page.Items) == 0 {
			return "没有客户端", nil
		}
		email, subID = page.Items[0].Email, page.Items[0].SubID
		return fmt.Sprintf("%d 个客户端", page.Total), nil
	})
	if email != "" {
		r.step("clients/traffic", func() (string, error) {
			t, err := r.panel.ClientTraffic(email)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("已用 %.2f GB", float64(t.Up+t.Down)/(1<<30)), nil
		})
	}
	if subID != "" {
		r.step("clients/subLinks", func() (string, error) {
			links, err := r.panel.SubLinks(subID)
			if err != nil {
				return "", err
			}
			if len(links) == 0 {
				return "该客户端没有分享链接", nil
			}
			return fmt.Sprintf("%d 条 · %s", len(links), maskLink(links[0])), nil
		})
		r.step("链接地址可用性", func() (string, error) {
			links, err := r.panel.SubLinks(subID)
			if err != nil {
				return "", err
			}
			for _, l := range links {
				if host := linkHost(l); isUnreachable(host) {
					return "", fmt.Errorf("链接指向 %s —— 客户端到不了这个地址", host)
				}
			}
			return "全部指向对外可达地址", nil
		})
	}

	fmt.Fprintf(r.out, "\n%d 项通过", r.pass)
	if r.fail > 0 {
		fmt.Fprintf(r.out, "，%d 项失败\n", r.fail)
		return fmt.Errorf("自检未全部通过")
	}
	fmt.Fprintln(r.out, "，全部通过")
	return nil
}

func (r *Runner) configTemplate() (map[string]any, error) {
	setting, err := r.panel.GetXraySetting()
	if err != nil {
		return nil, err
	}
	tpl, err := xraycfg.ParseTemplate(setting.Template)
	if err != nil {
		return nil, err
	}
	return tpl, nil
}

func (r *Runner) probeCategory(token string) (string, error) {
	file, code := "geosite.dat", token
	switch {
	case strings.HasPrefix(token, "geosite:"):
		code = token[len("geosite:"):]
	case strings.HasPrefix(token, "ext:"):
		parts := strings.SplitN(token, ":", 3)
		if len(parts) != 3 {
			return "", fmt.Errorf("ext 写法无法解析: %s", token)
		}
		file, code = parts[1], parts[2]
	}
	page, err := r.panel.GeodataEntries(file, code, "", 0, 5)
	if err != nil {
		return "", err
	}
	if len(page.Items) == 0 {
		return "", fmt.Errorf("分类 %s 报告 %d 条但没返回条目", token, page.Total)
	}
	return fmt.Sprintf("%s 共 %d 条 · 首条 %s=%s", token, page.Total, page.Items[0].Kind, page.Items[0].Value), nil
}

// firstCategory finds a geosite/ext token in the live rules to probe with.
func firstCategory(rules []xraycfg.Rule) string {
	for _, rule := range rules {
		items, _ := rule.Raw["domain"].([]any)
		for _, it := range items {
			s, _ := it.(string)
			if strings.HasPrefix(s, "geosite:") || strings.HasPrefix(s, "ext:") {
				return s
			}
		}
	}
	return ""
}

// linkHost pulls the authority out of a share link.
func linkHost(link string) string {
	_, rest, ok := strings.Cut(link, "://")
	if !ok {
		return ""
	}
	if _, after, ok := strings.Cut(rest, "@"); ok {
		rest = after
	}
	host, _, _ := strings.Cut(rest, "?")
	host, _, _ = strings.Cut(host, "#")
	host, _, _ = strings.Cut(host, "/")
	return host
}

// isUnreachable flags an address only the panel host itself can dial. A link
// carrying one is the failure that matters here: it looks fine and is useless
// the moment it reaches a client.
func isUnreachable(hostPort string) bool {
	host := hostPort
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	switch {
	case host == "", host == "localhost", strings.HasPrefix(host, "127."):
		return true
	case strings.HasSuffix(host, ".ts.net"):
		return true
	case strings.HasPrefix(host, "100."), strings.HasPrefix(host, "10."), strings.HasPrefix(host, "192.168."):
		return true
	}
	return false
}

func maskLink(link string) string {
	masked := secretish.ReplaceAllString(link, "${1}***${2}")
	if len(masked) > 60 {
		return masked[:60] + "…"
	}
	return masked
}
