// Package configtxn owns the staging area shared by every module that edits the
// Xray config template: review, apply, discard, and rollback.
package configtxn

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/state"
	"github.com/lichenhao/xui-remote/internal/txn"
	"github.com/lichenhao/xui-remote/internal/verify"
	"github.com/lichenhao/xui-remote/internal/xraycfg"
)

type Module struct {
	txn      *txn.Manager
	verifier *verify.Verifier
}

func New(t *txn.Manager, v *verify.Verifier) *Module { return &Module{txn: t, verifier: v} }

func (m *Module) Name() string  { return "config" }
func (m *Module) Title() string { return "⚙️ 配置" }

func (m *Module) Commands() []bot.Command {
	return []bot.Command{
		{Name: "diff", Description: "查看暂存的配置改动", Handler: func(c *bot.Context, _ string) error { return m.diff(c, false) }},
		{Name: "apply", Description: "提交暂存改动并重启 Xray", Handler: func(c *bot.Context, _ string) error { return m.confirmApply(c, false) }},
		{Name: "rollback", Description: "回滚到某个配置快照", Handler: func(c *bot.Context, _ string) error { return m.snapshots(c, false) }},
	}
}

func (m *Module) Menu(c *bot.Context) error { return m.diff(c, false) }

func (m *Module) Callback(c *bot.Context, action string, args []string) error {
	switch action {
	case "diff":
		return m.diff(c, true)
	case "apply":
		return m.confirmApply(c, true)
	case "applygo":
		return m.apply(c, false)
	case "applyforce":
		return m.apply(c, true)
	case "discard":
		return m.confirmDiscard(c)
	case "discardgo":
		if err := m.txn.Discard(); err != nil {
			return err
		}
		return c.Replace("已丢弃全部暂存改动。")
	case "snaps":
		return m.snapshots(c, true)
	case "rb":
		return m.confirmRollback(c, args)
	case "rbgo":
		return m.rollback(c, args)
	}
	return nil
}

func (m *Module) diff(c *bot.Context, replace bool) error {
	st, err := m.txn.Pending()
	if errors.Is(err, txn.ErrNothingStaged) {
		kb := bot.Keyboard(bot.InlineRow(bot.Button("配置快照", c.CallbackData("snaps"))))
		return send(c, replace, "<b>⚙️ 配置</b>\n\n暂存区是空的，当前配置即线上配置。", kb)
	}
	if err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>⚙️ 暂存区（%d 项）</b>\n\n", len(st.Entries)))
	for i, e := range st.Entries {
		sb.WriteString(fmt.Sprintf("%d. %s\n   <i>%s · %s</i>\n",
			i+1, bot.Escape(e.Summary), bot.Escape(e.Module), e.At.Format("01-02 15:04")))
	}
	sb.WriteString("\n<i>提交会重启 Xray，断开当前连接。</i>")

	kb := bot.Keyboard(
		bot.InlineRow(bot.Button("✅ 提交并重启", c.CallbackData("apply"))),
		bot.InlineRow(
			bot.Button("丢弃", c.CallbackData("discard")),
			bot.Button("配置快照", c.CallbackData("snaps")),
		),
	)
	return send(c, replace, sb.String(), kb)
}

func (m *Module) confirmApply(c *bot.Context, replace bool) error {
	st, err := m.txn.Pending()
	if errors.Is(err, txn.ErrNothingStaged) {
		return send(c, replace, "暂存区是空的，没有要提交的改动。", nil)
	}
	if err != nil {
		return err
	}
	body := fmt.Sprintf("确认提交 <b>%d</b> 项改动？\n\n<b>Xray 会重启，当前所有连接断开。</b>", len(st.Entries))
	return send(c, replace, body, bot.Keyboard(
		bot.InlineRow(bot.Button("确认提交", c.CallbackData("applygo"))),
		bot.InlineRow(bot.Button("取消", "core:cancel")),
	))
}

func (m *Module) apply(c *bot.Context, force bool) error {
	res, err := m.txn.Apply(force)
	if errors.Is(err, txn.ErrConflict) {
		// The live template moved under us — almost always someone editing in
		// the web panel. Overwriting would silently drop their change.
		return c.Replace(
			"⚠️ <b>面板侧配置已变更</b>\n\n有人在网页面板改过配置。现在提交会覆盖掉那些改动。\n\n"+
				"建议丢弃暂存区、重新做一遍改动。",
			bot.Keyboard(
				bot.InlineRow(bot.Button("仍然覆盖提交", c.CallbackData("applyforce"))),
				bot.InlineRow(bot.Button("丢弃暂存区", c.CallbackData("discard"))),
			))
	}
	if err != nil {
		return fmt.Errorf("提交失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("✅ <b>已提交，Xray 已重启</b>\n\n")
	for _, e := range res.Applied {
		sb.WriteString("• " + bot.Escape(e.Summary) + "\n")
	}
	sb.WriteString(fmt.Sprintf("\n快照 <code>%s</code> 已保存，可回滚。", res.Snapshot.ID))

	var checks []state.Check
	for _, e := range res.Applied {
		checks = append(checks, e.Checks...)
	}
	if len(checks) == 0 {
		return c.Replace(sb.String(), bot.Keyboard(
			bot.InlineRow(bot.Button("🧪 去测试路由", "rtest:menu")),
			bot.InlineRow(bot.Button("回滚这次改动", c.CallbackData("rb", res.Snapshot.ID))),
		))
	}
	// Report the apply before verifying: the probes take a few seconds each and
	// the operator should not be left wondering whether the write landed.
	sb.WriteString(fmt.Sprintf("\n\n正在验证 %d 项…", len(checks)))
	if err := c.Replace(sb.String()); err != nil {
		return err
	}
	return m.report(c, m.verifier.Run(checks), res.Snapshot.ID)
}

// report renders verification results and, for a rule that did not take
// effect, names the earlier rule that most likely swallowed it.
func (m *Module) report(c *bot.Context, results []verify.Result, snapshotID string) error {
	var sb strings.Builder
	passed, failed := 0, 0
	for _, r := range results {
		switch {
		case !r.Verified():
			sb.WriteString(fmt.Sprintf("➖ %s\n    <i>%s</i>\n", bot.Escape(r.Source), bot.Escape(r.Skipped)))
		case r.OK:
			passed++
			sb.WriteString(fmt.Sprintf("✅ %s\n    取样 %s → %s\n",
				bot.Escape(r.Source), bot.Escape(r.Target), bot.Escape(r.Actual)))
		default:
			failed++
			sb.WriteString(fmt.Sprintf("❌ %s\n    取样 %s → <b>%s</b>，期望 <b>%s</b>\n",
				bot.Escape(r.Source), bot.Escape(r.Target), bot.Escape(r.Actual), bot.Escape(r.Expect)))
			if idx, ok := m.shadowedBy(r); ok {
				sb.WriteString(fmt.Sprintf("    <i>可能被第 %d 条规则遮蔽</i>\n", idx+1))
			}
		}
	}

	head := fmt.Sprintf("<b>验证 %d/%d 通过</b>\n\n", passed, passed+failed)
	if failed == 0 {
		return c.Send(head + sb.String())
	}

	var rows [][]telego.InlineKeyboardButton
	if idx, ok := failingRuleIndex(results); ok {
		rows = append(rows, bot.InlineRow(
			bot.Button(fmt.Sprintf("调整第 %d 条的位置", idx+1), bot.CallbackData("route", "moveto", strconv.Itoa(idx)))))
	}
	rows = append(rows, bot.InlineRow(bot.Button("回滚这次改动", c.CallbackData("rb", snapshotID))))
	return c.Send("⚠️ "+head+sb.String()+
		"\n<i>规则已写入但没生效。分类之间的重叠算不出来，所以这只能在提交后发现。</i>",
		bot.Keyboard(rows...))
}

// shadowedBy looks for an earlier rule pointing at the outbound the traffic
// actually took — the usual explanation for a rule that never fires.
func (m *Module) shadowedBy(r verify.Result) (int, bool) {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return 0, false
	}
	rules, err := xraycfg.Rules(tpl)
	if err != nil {
		return 0, false
	}
	for _, rule := range rules {
		if rule.Index >= r.Index {
			break
		}
		if rule.Target() == r.Actual {
			return rule.Index, true
		}
	}
	return 0, false
}

func failingRuleIndex(results []verify.Result) (int, bool) {
	for _, r := range results {
		if r.Verified() && !r.OK {
			return r.Index, true
		}
	}
	return 0, false
}

func (m *Module) confirmDiscard(c *bot.Context) error {
	return c.Replace("确认丢弃全部暂存改动？", bot.Keyboard(
		bot.InlineRow(bot.Button("确认丢弃", c.CallbackData("discardgo"))),
		bot.InlineRow(bot.Button("取消", c.CallbackData("diff"))),
	))
}

func (m *Module) snapshots(c *bot.Context, replace bool) error {
	snaps, err := m.txn.Snapshots()
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return send(c, replace, "还没有配置快照。每次提交前会自动存一份。", nil)
	}

	var sb strings.Builder
	sb.WriteString("<b>🕓 配置快照</b>\n<i>快照记录的是提交前的线上配置。</i>\n\n")
	var rows [][]telego.InlineKeyboardButton
	for i, s := range snaps {
		if i >= 10 {
			break
		}
		sb.WriteString(fmt.Sprintf("<code>%s</code>  %s\n   %s\n",
			s.ID, humanAge(s.At), bot.Escape(truncate(s.Summary, 80))))
		rows = append(rows, bot.InlineRow(bot.Button("回滚到 "+s.ID, c.CallbackData("rb", s.ID))))
	}
	sb.WriteString("\n<i>回滚是整份写回，会一并撤销该快照之后的所有配置改动。\n入站、客户端、出站订阅不在快照范围内。</i>")
	return send(c, replace, sb.String(), bot.Keyboard(rows...))
}

func (m *Module) confirmRollback(c *bot.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return c.Replace(
		fmt.Sprintf("确认回滚到快照 <code>%s</code>？\n\n<b>会整份写回配置并重启 Xray</b>，该快照之后的所有配置改动都会被撤销。", args[0]),
		bot.Keyboard(
			bot.InlineRow(bot.Button("确认回滚", c.CallbackData("rbgo", args[0]))),
			bot.InlineRow(bot.Button("取消", c.CallbackData("snaps"))),
		))
}

func (m *Module) rollback(c *bot.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if err := m.txn.Rollback(args[0]); err != nil {
		return fmt.Errorf("回滚失败: %w", err)
	}
	return c.Replace(fmt.Sprintf("✅ 已回滚到 <code>%s</code>，Xray 已重启。\n\n回滚前的状态也存了快照，可以再滚回去。", args[0]))
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

func humanAge(t time.Time) string {
	d := time.Since(t)
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

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
