// Package inbound lists inbounds and toggles them. Inbounds live in the panel
// database, not the config template, so nothing here is staged or snapshotted.
package inbound

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/panel"
)

type Module struct {
	panel *panel.Client
}

func New(p *panel.Client) *Module { return &Module{panel: p} }

func (m *Module) Name() string  { return "in" }
func (m *Module) Title() string { return "🚪 入站" }

func (m *Module) Commands() []bot.Command {
	return []bot.Command{{
		Name:        "inbounds",
		Description: "入站列表与启停",
		Handler:     func(c *bot.Context, _ string) error { return m.list(c, false) },
	}}
}

func (m *Module) Menu(c *bot.Context) error { return m.list(c, false) }

func (m *Module) Callback(c *bot.Context, action string, args []string) error {
	switch action {
	case "list":
		return m.list(c, true)
	case "toggle":
		return m.confirmToggle(c, args)
	case "togglego":
		return m.toggle(c, args)
	}
	return nil
}

func (m *Module) list(c *bot.Context, replace bool) error {
	inbounds, err := m.panel.ListInbounds()
	if err != nil {
		return fmt.Errorf("读取入站失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("<b>🚪 入站</b>\n\n")
	if len(inbounds) == 0 {
		sb.WriteString("<i>没有入站。</i>\n")
	}
	var rows [][]telego.InlineKeyboardButton
	for _, in := range inbounds {
		state := "✅"
		if !in.Enable {
			state = "⛔️"
		}
		name := in.Remark
		if name == "" {
			name = in.Tag
		}
		sb.WriteString(fmt.Sprintf("%s <b>%s</b>\n    %s:%d · %s · ↑%s ↓%s\n",
			state, bot.Escape(name), bot.Escape(in.Listen), in.Port,
			bot.Escape(in.Protocol), humanBytes(in.Up), humanBytes(in.Down)))

		label := "停用 " + truncate(name, 12)
		if !in.Enable {
			label = "启用 " + truncate(name, 12)
		}
		rows = append(rows, bot.InlineRow(
			bot.Button(label, c.CallbackData("toggle", strconv.Itoa(in.Id), strconv.FormatBool(!in.Enable)))))
	}
	sb.WriteString("\n<i>入站不在配置快照范围内，启停立即生效、不进暂存区。\n改端口请在面板操作 —— 需要完整对象往返，API 上做有损坏入站的风险。</i>")

	rows = append(rows, bot.InlineRow(bot.Home()))
	if replace {
		return c.Replace(sb.String(), bot.Keyboard(rows...))
	}
	return c.Send(sb.String(), bot.Keyboard(rows...))
}

func (m *Module) confirmToggle(c *bot.Context, args []string) error {
	if len(args) < 2 {
		return nil
	}
	verb := "停用"
	if args[1] == "true" {
		verb = "启用"
	}
	return c.Replace(
		fmt.Sprintf("确认%s这个入站？\n\n<b>%s会立即生效并可能重启 Xray。</b>", verb, verb),
		bot.Keyboard(
			bot.InlineRow(bot.Button("确认"+verb, c.CallbackData("togglego", args[0], args[1]))),
			bot.InlineRow(bot.Button("取消", c.CallbackData("list"))),
		))
}

func (m *Module) toggle(c *bot.Context, args []string) error {
	if len(args) < 2 {
		return nil
	}
	id, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("入站 id 无效")
	}
	if err := m.panel.SetInboundEnable(id, args[1] == "true"); err != nil {
		return fmt.Errorf("操作失败: %w", err)
	}
	return m.list(c, true)
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
