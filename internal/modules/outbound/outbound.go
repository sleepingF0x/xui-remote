// Package outbound manages the template's outbounds, their health, remote
// outbound subscriptions, and runtime balancer overrides.
package outbound

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/panel"
	"github.com/lichenhao/xui-remote/internal/txn"
	"github.com/lichenhao/xui-remote/internal/xraycfg"
)

// probeSkipProtocols never carry real traffic, so probing them only produces
// noise in the health report.
var probeSkipProtocols = map[string]bool{"blackhole": true, "dns": true}

type Module struct {
	panel *panel.Client
	txn   *txn.Manager
}

func New(p *panel.Client, t *txn.Manager) *Module { return &Module{panel: p, txn: t} }

func (m *Module) Name() string  { return "out" }
func (m *Module) Title() string { return "📡 出站" }

func (m *Module) Commands() []bot.Command {
	return []bot.Command{
		{Name: "outbounds", Description: "出站列表与订阅", Handler: func(c *bot.Context, _ string) error { return m.overview(c, false) }},
		{Name: "health", Description: "测试所有出站连通性", Handler: func(c *bot.Context, _ string) error { return m.health(c, false) }},
	}
}

func (m *Module) Menu(c *bot.Context) error { return m.overview(c, false) }

func (m *Module) Callback(c *bot.Context, action string, args []string) error {
	switch action {
	case "menu":
		return m.overview(c, true)
	case "health":
		return m.health(c, true)
	case "view":
		return m.view(c, atoi(args, 0))
	case "del":
		return m.confirmDelete(c, atoi(args, 0))
	case "delgo":
		return m.deleteOutbound(c, atoi(args, 0))
	case "bal":
		return m.balancers(c)
	case "balpick":
		return m.balancerTargets(c, args)
	case "balset":
		return m.setBalancer(c, args)
	case "subs":
		return m.listSubs(c, true)
	case "subnew":
		return m.askSubURL(c)
	case "subadd":
		return m.addSub(c, args)
	case "subref":
		return m.refreshSub(c, atoi(args, 0))
	case "subtoggle":
		return m.toggleSub(c, atoi(args, 0))
	case "subdel":
		return m.confirmDeleteSub(c, atoi(args, 0))
	case "subdelgo":
		return m.deleteSub(c, atoi(args, 0))
	}
	return nil
}

func (m *Module) overview(c *bot.Context, replace bool) error {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return err
	}
	obs := xraycfg.Outbounds(tpl)

	var sb strings.Builder
	sb.WriteString("<b>📡 出站</b>\n\n")
	sb.WriteString(fmt.Sprintf("<b>模板出站 (%d)</b>\n", len(obs)))
	for _, ob := range obs {
		tag := ob.Tag
		if tag == "" {
			tag = "(无 tag)"
		}
		def := ""
		if ob.Index == 0 {
			def = "  ← 默认出站"
		}
		sb.WriteString(fmt.Sprintf("  %d. %s <i>%s</i>%s\n", ob.Index+1, bot.Escape(tag), bot.Escape(ob.Protocol), def))
	}

	subs, subErr := m.panel.ListOutboundSubs()
	if subErr == nil && len(subs) > 0 {
		sb.WriteString(fmt.Sprintf("\n<b>订阅 (%d)</b>\n", len(subs)))
		for _, s := range subs {
			sb.WriteString("  " + subLine(s) + "\n")
		}
	}
	sb.WriteString("\n<i>订阅节点由面板在生成配置时合并，不在模板里，也不在配置快照范围内。</i>")

	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton
	for _, ob := range obs {
		row = append(row, bot.Button(strconv.Itoa(ob.Index+1), c.CallbackData("view", strconv.Itoa(ob.Index))))
		if len(row) == 6 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows,
		bot.InlineRow(
			bot.Button("🩺 健康检查", c.CallbackData("health")),
			bot.Button("⚖️ 负载均衡", c.CallbackData("bal")),
		),
		bot.InlineRow(
			bot.Button("📥 订阅管理", c.CallbackData("subs")),
			bot.Home(),
		),
	)
	return send(c, replace, sb.String(), bot.Keyboard(rows...))
}

func (m *Module) health(c *bot.Context, replace bool) error {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return err
	}
	obs := xraycfg.Outbounds(tpl)

	var probe []json.RawMessage
	var labels []string
	for _, ob := range obs {
		if probeSkipProtocols[ob.Protocol] {
			continue
		}
		raw, err := json.Marshal(ob.Raw)
		if err != nil {
			continue
		}
		probe = append(probe, raw)
		labels = append(labels, ob.Tag)
	}
	if len(probe) == 0 {
		return c.Send("没有可测试的出站。")
	}

	all, _ := json.Marshal(tpl["outbounds"])
	if err := send(c, replace, fmt.Sprintf("正在测试 %d 个出站…", len(probe)), nil); err != nil {
		return err
	}
	results, err := m.panel.TestOutbounds(probe, all, "")
	if err != nil {
		return fmt.Errorf("测试失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("<b>🩺 出站连通性</b>\n\n")
	for i, r := range results {
		label := r.Tag
		if label == "" && i < len(labels) {
			label = labels[i]
		}
		if r.Success {
			sb.WriteString(fmt.Sprintf("✅ %-16s %d ms\n", bot.Escape(label), r.Delay))
			continue
		}
		reason := r.Error
		if reason == "" {
			reason = "失败"
		}
		sb.WriteString(fmt.Sprintf("❌ %-16s %s\n", bot.Escape(label), bot.Escape(truncate(reason, 60))))
	}
	return c.Send(sb.String(), bot.Keyboard(
		bot.InlineRow(bot.Button("重新测试", c.CallbackData("health"))),
		bot.InlineRow(bot.Button("← 返回", c.CallbackData("menu"))),
	))
}

func (m *Module) view(c *bot.Context, idx int) error {
	obs, err := m.outbounds()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(obs) {
		return fmt.Errorf("出站 %d 不存在", idx+1)
	}
	ob := obs[idx]
	body := fmt.Sprintf("<b>出站 %d — %s</b>\n\n%s",
		idx+1, bot.Escape(ob.Tag), bot.Code(truncate(xraycfg.RawJSON(ob.Raw), 3000)))
	return c.Replace(body, bot.Keyboard(
		bot.InlineRow(bot.Button("删除", c.CallbackData("del", strconv.Itoa(idx)))),
		bot.InlineRow(bot.Button("← 返回", c.CallbackData("menu"))),
	))
}

func (m *Module) confirmDelete(c *bot.Context, idx int) error {
	obs, err := m.outbounds()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(obs) {
		return fmt.Errorf("出站 %d 不存在", idx+1)
	}
	warn := ""
	if idx == 0 {
		warn = "\n\n⚠️ <b>这是默认出站</b>，删掉后未匹配规则的流量会改走新的第一条。"
	}
	if refs := m.rulesReferencing(obs[idx].Tag); len(refs) > 0 {
		warn += fmt.Sprintf("\n\n⚠️ 有 <b>%d</b> 条路由规则指向它，删掉后这些规则会指向不存在的出站。", len(refs))
	}
	return c.Replace(
		fmt.Sprintf("删除出站 <b>%s</b>？%s", bot.Escape(obs[idx].Tag), warn),
		bot.Keyboard(
			bot.InlineRow(bot.Button("确认删除", c.CallbackData("delgo", strconv.Itoa(idx)))),
			bot.InlineRow(bot.Button("取消", c.CallbackData("view", strconv.Itoa(idx)))),
		))
}

func (m *Module) deleteOutbound(c *bot.Context, idx int) error {
	obs, err := m.outbounds()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(obs) {
		return fmt.Errorf("出站 %d 不存在", idx+1)
	}
	summary := fmt.Sprintf("删除出站 %s", obs[idx].Tag)
	err = m.txn.Edit("outbound", summary, func(tpl map[string]any) error {
		raw, ok := tpl["outbounds"].([]any)
		if !ok || idx >= len(raw) {
			return fmt.Errorf("出站 %d 不存在", idx+1)
		}
		tpl["outbounds"] = append(append([]any{}, raw[:idx]...), raw[idx+1:]...)
		return nil
	})
	if err != nil {
		return err
	}
	return c.Replace("✅ 已暂存："+bot.Escape(summary)+"\n\n<i>还未生效，到「⚙️ 配置」提交。</i>",
		bot.Keyboard(
			bot.InlineRow(bot.Button("查看暂存区", "config:diff")),
			bot.InlineRow(bot.Button("← 返回", c.CallbackData("menu"))),
		))
}

// rulesReferencing finds routing rules whose target is tag, so deleting an
// outbound can warn about the rules it would strand.
func (m *Module) rulesReferencing(tag string) []int {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return nil
	}
	rules, err := xraycfg.Rules(tpl)
	if err != nil {
		return nil
	}
	var out []int
	for _, r := range rules {
		if t, _ := r.Raw["outboundTag"].(string); t == tag {
			out = append(out, r.Index)
		}
	}
	return out
}

func (m *Module) outbounds() ([]xraycfg.Outbound, error) {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return nil, err
	}
	return xraycfg.Outbounds(tpl), nil
}

func send(c *bot.Context, replace bool, text string, kb *telego.InlineKeyboardMarkup) error {
	if replace {
		if kb == nil {
			return c.Replace(text)
		}
		return c.Replace(text, kb)
	}
	if kb == nil {
		return c.Send(text)
	}
	return c.Send(text, kb)
}

func atoi(args []string, i int) int {
	if i >= len(args) {
		return -1
	}
	n, err := strconv.Atoi(args[i])
	if err != nil {
		return -1
	}
	return n
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func humanAge(unixSeconds int64) string {
	if unixSeconds == 0 {
		return "从未更新"
	}
	d := time.Since(time.Unix(unixSeconds, 0))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(d.Hours())/24)
	}
}
