// Package status reports panel and Xray health and can restart the core.
package status

import (
	"fmt"
	"strings"
	"time"

	"github.com/sleepingF0x/xui-remote/internal/bot"
	"github.com/sleepingF0x/xui-remote/internal/panel"
)

type Module struct {
	panel *panel.Client
}

func New(p *panel.Client) *Module { return &Module{panel: p} }

func (m *Module) Name() string  { return "status" }
func (m *Module) Title() string { return "📊 状态" }

func (m *Module) Commands() []bot.Command {
	return []bot.Command{{
		Name:        "status",
		Description: "面板与 Xray 运行状态",
		Handler:     func(c *bot.Context, _ string) error { return m.render(c, false) },
	}}
}

func (m *Module) Menu(c *bot.Context) error { return m.render(c, false) }

func (m *Module) Callback(c *bot.Context, action string, args []string) error {
	switch action {
	case "refresh":
		return m.render(c, true)
	case "log":
		return m.showXrayLog(c)
	case "restart":
		return c.Replace("确认重启 Xray？\n\n<b>会断开当前所有连接。</b>",
			bot.Keyboard(
				bot.InlineRow(bot.Button("确认重启", c.CallbackData("restart_go"))),
				bot.InlineRow(bot.Button("取消", "core:cancel")),
			))
	case "restart_go":
		if err := m.panel.RestartXray(); err != nil {
			return fmt.Errorf("重启失败: %w", err)
		}
		// The core needs a moment before /server/status reports the new state.
		time.Sleep(2 * time.Second)
		return m.render(c, true)
	}
	return nil
}

func (m *Module) render(c *bot.Context, replace bool) error {
	s, err := m.panel.Status()
	if err != nil {
		return fmt.Errorf("读取状态失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("<b>📊 运行状态</b>\n\n")
	sb.WriteString(fmt.Sprintf("Xray      %s  %s\n", stateIcon(s.Xray.State), bot.Escape(s.Xray.Version)))
	sb.WriteString(fmt.Sprintf("面板      v%s\n", bot.Escape(s.PanelVersion)))
	sb.WriteString(fmt.Sprintf("运行时长  %s\n", humanUptime(s.Uptime)))
	sb.WriteString(fmt.Sprintf("CPU       %.1f%%\n", s.Cpu))
	sb.WriteString(fmt.Sprintf("内存      %s / %s\n", humanBytes(s.Mem.Current), humanBytes(s.Mem.Total)))
	sb.WriteString(fmt.Sprintf("TCP 连接  %d\n", s.TcpCount))
	if s.Xray.ErrorMsg != "" {
		sb.WriteString("\n⚠️ <b>Xray 报错</b>\n" + bot.Code(truncate(s.Xray.ErrorMsg, 500)))
	}

	kb := bot.Keyboard(
		bot.InlineRow(
			bot.Button("刷新", c.CallbackData("refresh")),
			bot.Button("错误输出", c.CallbackData("log")),
		),
		bot.InlineRow(
			bot.Button("重启 Xray", c.CallbackData("restart")),
			bot.Home(),
		),
	)
	if replace {
		return c.Replace(sb.String(), kb)
	}
	return c.Send(sb.String(), kb)
}

func (m *Module) showXrayLog(c *bot.Context) error {
	out, err := m.panel.XrayResult()
	if err != nil {
		return fmt.Errorf("读取 Xray 输出失败: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return c.Send("Xray 没有错误输出。",
			bot.Keyboard(bot.InlineRow(bot.Back(c, "refresh"))))
	}
	return c.Send("<b>Xray 错误输出</b>\n"+bot.Code(out),
		bot.Keyboard(bot.InlineRow(bot.Back(c, "refresh"))))
}

func stateIcon(state string) string {
	switch strings.ToLower(state) {
	case "running":
		return "✅ 运行中"
	case "stop", "stopped":
		return "⛔️ 已停止"
	default:
		return "⚠️ " + bot.Escape(state)
	}
}

func humanUptime(seconds uint64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	}
	return fmt.Sprintf("%d 小时 %d 分", hours, int(d.Minutes())%60)
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
