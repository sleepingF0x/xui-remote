// Package routetest asks the running core which outbound a destination would
// take. It reflects the live config only — an unapplied draft cannot be tested.
package routetest

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/detect"
	"github.com/lichenhao/xui-remote/internal/panel"
)

const (
	defaultPort    = 443
	defaultNetwork = "tcp"
)

type Module struct {
	panel *panel.Client
}

func New(p *panel.Client) *Module { return &Module{panel: p} }

func (m *Module) Name() string  { return "rtest" }
func (m *Module) Title() string { return "🧪 路由测试" }

func (m *Module) Commands() []bot.Command {
	return []bot.Command{{
		Name:        "test",
		Description: "测试域名或 IP 命中哪个出站",
		Handler: func(c *bot.Context, args string) error {
			if args == "" {
				return m.Menu(c)
			}
			return m.run(c, args, defaultPort, defaultNetwork, false)
		},
	}}
}

func (m *Module) Menu(c *bot.Context) error {
	c.Await("target")
	return c.Send("发送要测试的<b>域名</b>或 <b>IP</b>：",
		bot.Keyboard(bot.InlineRow(bot.Home())))
}

func (m *Module) Prompt(c *bot.Context, key, text string) error {
	if key != "target" {
		return nil
	}
	return m.run(c, text, defaultPort, defaultNetwork, false)
}

// MatchText offers a route lookup for anything that names a destination.
func (m *Module) MatchText(input string) []bot.TextOffer {
	r := detect.Classify(input)
	if !r.IsHostLike() && r.Kind != detect.KindCIDR {
		return nil
	}
	return []bot.TextOffer{{
		Label:    "查当前路由",
		Callback: bot.CallbackData("rtest", "ask", r.Value),
	}}
}

func (m *Module) Callback(c *bot.Context, action string, args []string) error {
	switch action {
	case "menu":
		return m.Menu(c)
	case "ask":
		// The dispatcher passes the value inline; it is short by construction
		// because it came from a classified host.
		if len(args) == 0 {
			return nil
		}
		return m.run(c, strings.Join(args, ":"), defaultPort, defaultNetwork, true)
	case "run":
		if len(args) < 3 {
			return nil
		}
		target, ok := c.Unstash(args[0])
		if !ok {
			return c.Replace("这个按钮已过期，请重新测试。")
		}
		port, _ := strconv.Atoi(args[1])
		return m.run(c, target, port, args[2], true)
	}
	return nil
}

func (m *Module) run(c *bot.Context, input string, port int, network string, replace bool) error {
	r := detect.Classify(input)
	if r.Value == "" {
		return fmt.Errorf("认不出 %q 是域名还是 IP", input)
	}

	req := panel.RouteTestRequest{Port: port, Network: network}
	switch r.Kind {
	case detect.KindIP:
		req.IP = r.Value
	case detect.KindCIDR:
		return fmt.Errorf("路由测试需要具体 IP，不能用网段")
	default:
		req.Domain = r.Value
	}

	res, err := m.panel.RouteTest(req)
	if err != nil {
		return fmt.Errorf("测试失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>%s</b>  (%s:%d)\n\n", bot.Escape(r.Value), bot.Escape(network), port))
	if !res.Matched {
		sb.WriteString("→ <b>未匹配任何规则</b>，走默认出站（outbounds 第一条）")
	} else {
		sb.WriteString("→ <b>" + bot.Escape(res.OutboundTag) + "</b>")
		if len(res.GroupTags) > 0 {
			sb.WriteString("\n经过负载均衡: " + bot.Escape(strings.Join(res.GroupTags, " → ")))
		}
	}

	token := c.Stash(r.Value)
	otherNet := "udp"
	if network == "udp" {
		otherNet = "tcp"
	}
	kb := bot.Keyboard(
		bot.InlineRow(
			bot.Button("80", c.CallbackData("run", token, "80", network)),
			bot.Button("443", c.CallbackData("run", token, "443", network)),
			bot.Button("改 "+otherNet, c.CallbackData("run", token, strconv.Itoa(port), otherNet)),
		),
		bot.InlineRow(
			bot.Button("重新测试", c.CallbackData("run", token, strconv.Itoa(port), network)),
			bot.Home(),
		),
	)
	if replace {
		return c.Replace(sb.String(), kb)
	}
	return c.Send(sb.String(), kb)
}
