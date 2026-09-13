// Package routetest asks the running core which outbound a destination would
// take. It reflects the live config only — an unapplied draft cannot be tested.
package routetest

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/detect"
	"github.com/lichenhao/xui-remote/internal/panel"
	"github.com/lichenhao/xui-remote/internal/xraycfg"
)

const (
	defaultPort    = 443
	defaultNetwork = "tcp"
	maxUserButtons = 12
)

type Module struct {
	panel *panel.Client
}

func New(p *panel.Client) *Module { return &Module{panel: p} }

func (m *Module) Name() string  { return "rtest" }
func (m *Module) Title() string { return "🧪 路由测试" }

// probe is the full set of parameters one test runs with. It is stashed as a
// whole because Telegram's callback data cannot carry a domain plus four
// fields, and because the user constraint has to survive every re-test.
type probe struct {
	Target  string `json:"t"`
	Port    int    `json:"p"`
	Network string `json:"n"`
	Email   string `json:"e"`
}

func (p probe) encode() string {
	raw, _ := json.Marshal(p)
	return string(raw)
}

func decodeProbe(s string) (probe, error) {
	var p probe
	err := json.Unmarshal([]byte(s), &p)
	return p, err
}

func (m *Module) Commands() []bot.Command {
	return []bot.Command{{
		Name:        "test",
		Description: "测试域名或 IP 命中哪个出站",
		Handler: func(c *bot.Context, args string) error {
			if args == "" {
				return m.Menu(c)
			}
			return m.run(c, probe{Target: args, Port: defaultPort, Network: defaultNetwork}, false)
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
	return m.run(c, probe{Target: text, Port: defaultPort, Network: defaultNetwork}, false)
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
		if len(args) == 0 {
			return nil
		}
		return m.run(c, probe{Target: strings.Join(args, ":"), Port: defaultPort, Network: defaultNetwork}, true)
	}

	if len(args) == 0 {
		return nil
	}
	raw, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新测试。")
	}
	p, err := decodeProbe(raw)
	if err != nil {
		return err
	}

	switch action {
	case "run":
		return m.run(c, p, true)
	case "askuser":
		return m.pickUser(c, p)
	case "user":
		if len(args) > 1 {
			if email, ok := c.Unstash(args[1]); ok {
				p.Email = email
			}
		} else {
			p.Email = ""
		}
		return m.run(c, p, true)
	}
	return nil
}

// pickUser lists clients as buttons: seven of this panel's rules are scoped to
// a user, and typing an address to test them would be the common case.
func (m *Module) pickUser(c *bot.Context, p probe) error {
	page, err := m.panel.SearchClients("", 1, maxUserButtons)
	if err != nil {
		return fmt.Errorf("读取客户端失败: %w", err)
	}
	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton
	for _, it := range page.Items {
		label := it.Email
		if it.Email == p.Email {
			label = "• " + label
		}
		row = append(row, bot.Button(label, c.CallbackData("user", c.Stash(p.encode()), c.Stash(it.Email))))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows,
		bot.InlineRow(bot.Button("不指定用户", c.CallbackData("user", c.Stash(p.encode())))),
		bot.InlineRow(bot.Back(c, "run", c.Stash(p.encode()))),
	)
	return c.Replace("以哪个用户的身份测试？\n\n<i>限定了 user 的规则，只有指定用户时才可能命中。</i>",
		bot.Keyboard(rows...))
}

func (m *Module) run(c *bot.Context, p probe, replace bool) error {
	r := detect.Classify(p.Target)
	if r.Value == "" {
		return fmt.Errorf("认不出 %q 是域名还是 IP", p.Target)
	}
	if r.Kind == detect.KindCIDR {
		return fmt.Errorf("路由测试需要具体 IP，不能用网段")
	}
	p.Target = r.Value
	if p.Port == 0 {
		p.Port = defaultPort
	}
	if p.Network == "" {
		p.Network = defaultNetwork
	}

	req := panel.RouteTestRequest{Port: p.Port, Network: p.Network, Email: p.Email}
	if r.Kind == detect.KindIP {
		req.IP = p.Target
	} else {
		req.Domain = p.Target
	}
	res, err := m.panel.RouteTest(req)
	if err != nil {
		return fmt.Errorf("测试失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("<b>" + bot.Escape(p.Target) + "</b>\n")
	sb.WriteString(bot.Escape(describe(p)) + "\n\n")
	if !res.Matched {
		sb.WriteString("→ <b>未匹配任何规则</b>，走默认出站（outbounds 第一条）")
	} else {
		sb.WriteString("→ <b>" + bot.Escape(res.OutboundTag) + "</b>")
		if len(res.GroupTags) > 0 {
			sb.WriteString("\n经过负载均衡: " + bot.Escape(strings.Join(res.GroupTags, " → ")))
		}
	}
	if n := m.userScopedRules(); p.Email == "" && n > 0 {
		sb.WriteString(fmt.Sprintf("\n\n⚠️ 有 <b>%d</b> 条规则限定了用户，不指定用户时它们永远匹配不上。", n))
	}

	token := c.Stash(p.encode())
	userLabel := "指定用户…"
	if p.Email != "" {
		userLabel = "换用户…"
	}
	kb := bot.Keyboard(
		bot.InlineRow(bot.Button(userLabel, c.CallbackData("askuser", token))),
		bot.InlineRow(
			bot.Button("重新测试", c.CallbackData("run", token)),
			bot.Home(),
		),
	)
	if replace {
		return c.Replace(sb.String(), kb)
	}
	return c.Send(sb.String(), kb)
}

// describe spells out the parameters the result was produced under, so a
// surprising answer can be traced to the inputs rather than to the rules.
func describe(p probe) string {
	user := "不指定用户"
	if p.Email != "" {
		user = "用户 " + p.Email
	}
	return fmt.Sprintf("%s · 端口 %d · %s", strings.ToUpper(p.Network), p.Port, user)
}

// userScopedRules counts rules that can only match when a probe names a user;
// without one they are silently unreachable and the result misleads.
func (m *Module) userScopedRules() int {
	setting, err := m.panel.GetXraySetting()
	if err != nil {
		return 0
	}
	tpl, err := xraycfg.ParseTemplate(setting.Template)
	if err != nil {
		return 0
	}
	rules, err := xraycfg.Rules(tpl)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rules {
		if narrows(r.Raw["user"]) {
			n++
		}
	}
	return n
}

func narrows(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	default:
		return true
	}
}
