package outbound

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/panel"
)

// MatchText offers subscription import for anything that looks like a fetchable
// list URL. The panel refuses what it cannot parse, so guessing here is cheap.
func (m *Module) MatchText(input string) []bot.TextOffer {
	s := strings.TrimSpace(input)
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return nil
	}
	return []bot.TextOffer{{
		Label:    "添加为出站订阅",
		Callback: bot.CallbackData("out", "subnew"),
	}}
}

func (m *Module) Prompt(c *bot.Context, key, text string) error {
	if key != "suburl" {
		return nil
	}
	rawURL := strings.TrimSpace(text)
	if !strings.HasPrefix(rawURL, "http") {
		return fmt.Errorf("这不像一个订阅链接")
	}
	if err := c.Send("正在拉取并解析…"); err != nil {
		return err
	}
	obs, err := m.panel.PreviewOutboundSub(rawURL)
	if err != nil {
		return fmt.Errorf("解析失败: %w", err)
	}
	if len(obs) == 0 {
		return fmt.Errorf("这个链接没解析出任何节点")
	}

	token := c.Stash(rawURL)
	body := fmt.Sprintf("解析到 <b>%d</b> 个节点。\n\n保存为订阅后，面板会定期自动刷新。", len(obs))
	return c.Send(body, bot.Keyboard(
		bot.InlineRow(bot.Button(fmt.Sprintf("确认添加 (%d 个节点)", len(obs)), c.CallbackData("subadd", token))),
		bot.InlineRow(bot.BackTo(c, "返回订阅列表", "subs")),
	))
}

func (m *Module) askSubURL(c *bot.Context) error {
	c.Await("suburl")
	return c.Send("发送订阅链接（机场订阅 URL）：",
		bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回订阅列表", "subs"))))
}

func (m *Module) addSub(c *bot.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	rawURL, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新发送链接。")
	}
	sub, err := m.panel.CreateOutboundSub(defaultRemark(rawURL), rawURL, "")
	if err != nil {
		return fmt.Errorf("添加失败: %w", err)
	}
	if _, err := m.panel.RefreshOutboundSub(sub.Id); err != nil {
		return fmt.Errorf("已添加但首次拉取失败: %w", err)
	}
	return c.Replace(
		fmt.Sprintf("✅ 订阅 <b>%s</b> 已添加并拉取。\n\n%s", bot.Escape(sub.Remark), restartNotice()),
		bot.Keyboard(
			bot.InlineRow(bot.Button("重启 Xray 使其生效", "status:restart")),
			bot.InlineRow(bot.Button("← 订阅列表", c.CallbackData("subs"))),
		))
}

func (m *Module) listSubs(c *bot.Context, replace bool) error {
	subs, err := m.panel.ListOutboundSubs()
	if err != nil {
		return fmt.Errorf("读取订阅失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("<b>📥 出站订阅</b>\n\n")
	if len(subs) == 0 {
		sb.WriteString("<i>还没有订阅。</i>\n")
	}
	var rows [][]telego.InlineKeyboardButton
	for _, s := range subs {
		sb.WriteString(subLine(s) + "\n")
		if s.LastError != "" {
			sb.WriteString("    ⚠️ " + bot.Escape(truncate(s.LastError, 80)) + "\n")
		}
		id := strconv.Itoa(s.Id)
		toggle := "停用"
		if !s.Enabled {
			toggle = "启用"
		}
		rows = append(rows, []telego.InlineKeyboardButton{
			bot.Button("🔄 "+truncate(s.Remark, 10), c.CallbackData("subref", id)),
			bot.Button(toggle, c.CallbackData("subtoggle", id)),
			bot.Button("删除", c.CallbackData("subdel", id)),
		})
	}
	rows = append(rows,
		bot.InlineRow(bot.Button("+ 添加订阅", c.CallbackData("subnew"))),
		bot.InlineRow(bot.Button("← 返回", c.CallbackData("menu"))),
	)
	return send(c, replace, sb.String(), bot.Keyboard(rows...))
}

func (m *Module) refreshSub(c *bot.Context, id int) error {
	obs, err := m.panel.RefreshOutboundSub(id)
	if err != nil {
		return fmt.Errorf("刷新失败: %w", err)
	}
	return c.Replace(
		fmt.Sprintf("✅ 已刷新，拉到 <b>%d</b> 个节点。\n\n%s", len(obs), restartNotice()),
		bot.Keyboard(
			bot.InlineRow(bot.Button("重启 Xray 使其生效", "status:restart")),
			bot.InlineRow(bot.Button("← 订阅列表", c.CallbackData("subs"))),
		))
}

func (m *Module) toggleSub(c *bot.Context, id int) error {
	subs, err := m.panel.ListOutboundSubs()
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.Id != id {
			continue
		}
		if err := m.panel.SetOutboundSubEnabled(s, !s.Enabled); err != nil {
			return fmt.Errorf("切换失败: %w", err)
		}
		return m.listSubs(c, true)
	}
	return fmt.Errorf("订阅 %d 不存在", id)
}

func (m *Module) confirmDeleteSub(c *bot.Context, id int) error {
	return c.Replace("确认删除这个订阅？它带来的所有节点都会消失。",
		bot.Keyboard(
			bot.InlineRow(bot.Button("确认删除", c.CallbackData("subdelgo", strconv.Itoa(id)))),
			bot.InlineRow(bot.Button("取消", c.CallbackData("subs"))),
		))
}

func (m *Module) deleteSub(c *bot.Context, id int) error {
	if err := m.panel.DeleteOutboundSub(id); err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	return c.Replace("✅ 订阅已删除。\n\n"+restartNotice(), bot.Keyboard(
		bot.InlineRow(bot.Button("重启 Xray 使其生效", "status:restart")),
		bot.InlineRow(bot.Button("← 订阅列表", c.CallbackData("subs"))),
	))
}

func subLine(s panel.OutboundSub) string {
	state := "✅"
	if !s.Enabled {
		state = "⏸"
	}
	if s.LastError != "" {
		state = "⚠️"
	}
	return fmt.Sprintf("%s <b>%s</b>  %d 节点 · %s",
		state, bot.Escape(s.Remark), s.OutboundCount, humanAge(s.LastUpdated))
}

// restartNotice states the one thing the subscription endpoints do not do: the
// panel only flags xray as needing a restart, it does not perform one.
func restartNotice() string {
	return "<i>订阅节点要等 Xray 重启后才会进入运行配置。</i>"
}

func defaultRemark(rawURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if i := strings.IndexAny(s, "/?"); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return "订阅"
	}
	return truncate(s, 40)
}
