// Package configtxn owns the staging area shared by every module that edits the
// Xray config template: review, apply, discard, and rollback.
package configtxn

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/sleepingF0x/xui-remote/internal/bot"
	"github.com/sleepingF0x/xui-remote/internal/txn"
	"github.com/sleepingF0x/xui-remote/internal/verify"
	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
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
	case "apply", "applygo", "applyforce", "discard", "discardgo", "rb", "rbgo":
		if err := m.txn.CheckVersion(c.Revision); err != nil {
			return c.Expired()
		}
	}
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
		if err := m.txn.DiscardAt(c.Revision); err != nil {
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
	view, viewErr := m.txn.View()
	if viewErr != nil {
		return viewErr
	}
	c.Revision = view.Version

	st := view.Staging
	if st == nil {
		kb := bot.Keyboard(bot.InlineRow(
			bot.Button("配置快照", c.CallbackData("snaps")),
			bot.Home(),
		))
		return send(c, replace, "<b>⚙️ 配置</b>\n\n暂存区是空的，当前配置即线上配置。", kb)
	}
	changes, err := xraycfg.Changes(st.Base, st.Draft, st.RuleOrigins)
	if err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>⚙️ 最终变化（%d 项）</b>\n\n", len(changes)))
	if len(changes) == 0 {
		sb.WriteString("没有实际变化，无需提交。可丢弃暂存记录。")
	} else {
		sb.WriteString(describeChanges(changes))
		sb.WriteString("\n<i>草稿尚未生效。提交会重启 Xray，断开当前连接。</i>")
	}

	kb := bot.Keyboard(
		bot.InlineRow(bot.Button("✅ 提交并重启", c.CallbackData("apply"))),
		bot.InlineRow(
			bot.Button("丢弃", c.CallbackData("discard")),
			bot.Button("配置快照", c.CallbackData("snaps")),
		),
		bot.InlineRow(bot.Home()),
	)
	if len(changes) == 0 {
		kb.InlineKeyboard = kb.InlineKeyboard[1:]
	}
	return send(c, replace, sb.String(), kb)
}

func (m *Module) confirmApply(c *bot.Context, replace bool) error {
	view, viewErr := m.txn.View()
	if viewErr != nil {
		return viewErr
	}
	c.Revision = view.Version

	st := view.Staging
	if st == nil {
		return send(c, replace, "暂存区是空的，没有要提交的改动。",
			bot.Keyboard(bot.InlineRow(bot.Home())))
	}
	changes, err := xraycfg.Changes(st.Base, st.Draft, st.RuleOrigins)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return send(c, replace, txn.ErrNoChanges.Error(), bot.Keyboard(bot.InlineRow(bot.Button("返回暂存区", "config:diff"))))
	}
	body := fmt.Sprintf("确认提交 <b>%d</b> 项改动？\n\n<b>Xray 会重启，当前所有连接断开。</b>", len(changes))
	return send(c, replace, body, bot.Keyboard(
		bot.InlineRow(bot.Button("确认提交", c.CallbackData("applygo"))),
		bot.InlineRow(bot.Button("取消", "config:diff")),
	))
}

func (m *Module) apply(c *bot.Context, force bool) error {
	res, err := m.txn.ApplyAt(c.Revision, force)
	if errors.Is(err, txn.ErrConflict) {
		// The live template moved under us — almost always someone editing in
		// the web panel. Overwriting would silently drop their change.
		return c.Replace(
			"⚠️ <b>面板侧配置已变更</b>\n\n有人在网页面板改过配置。现在提交会覆盖掉那些改动。\n\n"+
				"建议丢弃暂存区、重新做一遍改动。",
			bot.Keyboard(
				bot.InlineRow(bot.Button("仍然覆盖提交", c.CallbackData("applyforce"))),
				bot.InlineRow(bot.Button("丢弃暂存区", c.CallbackData("discard"))),
				bot.InlineRow(bot.BackTo(c, "返回暂存区", "diff")),
			))
	}
	if err != nil {
		return fmt.Errorf("提交失败: %w", err)
	}

	c.Revision = res.Version
	var sb strings.Builder
	sb.WriteString("✅ <b>已提交，Xray 已重启</b>\n\n")
	changes, _ := xraycfg.Changes(res.Base, res.Draft, res.RuleOrigins)
	sb.WriteString(describeChanges(changes))
	sb.WriteString(fmt.Sprintf("\n快照 <code>%s</code> 已保存，可回滚。", res.Snapshot.ID))
	checks, planErr := verify.Plan(res.Base, res.Draft, res.RuleOrigins)
	if planErr != nil {
		return c.Replace(sb.String() + "\n验证计划无法生成：" + bot.Escape(planErr.Error()))
	}
	if len(checks) == 0 {
		return c.Replace(sb.String(), bot.Keyboard(
			bot.InlineRow(bot.Button("🧪 去测试路由", "rtest:menu")),
			bot.InlineRow(bot.Button("回滚这次改动", c.CallbackData("rb", res.Snapshot.ID))),
			bot.InlineRow(bot.Home()),
		))
	}
	// Report the apply before verifying: the probes take a few seconds each and
	// the operator should not be left wondering whether the write landed.
	sb.WriteString(fmt.Sprintf("\n\n正在验证 %d 项…", len(checks)))
	if err := c.Replace(sb.String()); err != nil {
		return err
	}
	results := m.verifier.Run(checks)
	if err := m.txn.CheckVersion(res.Version); err != nil {
		for i := range results {
			results[i].Skipped = "配置已再次变化，取样不能归属本次提交，请重新测试"
		}
	}
	return m.report(c, results, res.Snapshot.ID)
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
			sb.WriteString(fmt.Sprintf("✅ %s\n    取样 %s%s → %s\n",
				bot.Escape(r.Source), bot.Escape(r.Target), asUser(r), bot.Escape(r.Actual)))
		default:
			failed++
			sb.WriteString(fmt.Sprintf("❌ %s\n    取样 %s%s → <b>%s</b>，期望 <b>%s</b>\n",
				bot.Escape(r.Source), bot.Escape(r.Target), asUser(r),
				bot.Escape(r.Actual), bot.Escape(r.Expect)))

		}
	}

	head := fmt.Sprintf("<b>验证 %d/%d 通过 · %d 项跳过</b>\n<i>取样结果仅代表本次提交，不能代表所有流量。</i>\n\n", passed, passed+failed, len(results)-passed-failed)
	if failed == 0 {
		return c.Send(head+sb.String(), bot.Keyboard(bot.InlineRow(bot.Home())))
	}

	routeContext := *c
	routeContext.Module = "route"
	routeContext.ReturnTo = ""
	var rows [][]telego.InlineKeyboardButton
	if idx, ok := failingRuleIndex(results); ok {
		rows = append(rows, bot.InlineRow(
			bot.Button(fmt.Sprintf("调整第 %d 条的位置", idx+1), routeContext.CallbackData("moveto", strconv.Itoa(idx)))))
	}
	rows = append(rows,
		bot.InlineRow(bot.Button("回滚这次改动", c.CallbackData("rb", snapshotID))),
		bot.InlineRow(bot.Home()),
	)
	return c.Send("⚠️ "+head+sb.String()+"\n<i>"+failureNote(false)+"</i>", bot.Keyboard(rows...))
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
	view, viewErr := m.txn.View()
	if viewErr != nil {
		return viewErr
	}
	c.Revision = view.Version

	snaps, err := m.txn.Snapshots()
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return send(c, replace, "还没有配置快照。每次提交前会自动存一份。",
			bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回配置", "diff"))))
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
	rows = append(rows, bot.InlineRow(bot.BackTo(c, "返回配置", "diff")))
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
	if err := m.txn.RollbackAt(c.Revision, args[0]); err != nil {
		return fmt.Errorf("回滚失败: %w", err)
	}
	return c.Replace(
		fmt.Sprintf("✅ 已回滚到 <code>%s</code>，Xray 已重启。\n\n回滚前的状态也存了快照，可以再滚回去。", args[0]),
		bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回快照列表", "snaps"))))
}

func asUser(r verify.Result) string {
	if r.User == "" {
		return ""
	}
	return "（以 " + bot.Escape(r.User) + " 身份）"
}

// failureNote only blames shadowing when an earlier rule was actually found
// pointing at the outbound the traffic took; otherwise it states the fact and
// leaves the cause open rather than asserting one.
func failureNote(shadowed bool) string {
	if shadowed {
		return "规则已写入但没生效，被更靠前的规则接走了。分类之间的重叠算不出来，所以这只能在提交后发现。"
	}
	return "规则已写入，但取样流量没走到期望的出站。可能是规则还有其他未满足的约束，" +
		"或取样到的域名并不真正属于该分类。"
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

func describeChanges(changes []xraycfg.Change) string {
	var sb strings.Builder
	for _, ch := range changes {
		switch {
		case ch.Before == nil:
			sb.WriteString(fmt.Sprintf("<b>新增%s · 第 %d 位</b>\n%s\n", ch.Kind, ch.AfterIndex+1, bot.Code(xraycfg.RawJSON(ch.After))))
		case ch.After == nil:
			sb.WriteString(fmt.Sprintf("<b>删除%s · 原第 %d 位</b>\n%s\n", ch.Kind, ch.BeforeIndex+1, bot.Code(xraycfg.RawJSON(ch.Before))))
		default:
			label := "修改"
			if ch.Moved {
				label = "顺序调整 / 修改"
			}
			sb.WriteString(fmt.Sprintf("<b>%s%s · 第 %d → %d 位</b>\n", label, ch.Kind, ch.BeforeIndex+1, ch.AfterIndex+1))
			if ch.Kind == "规则" {
				r := xraycfg.Rule{Raw: ch.After}
				sb.WriteString(bot.Escape(r.Summary()+" → "+r.Target()) + "\n")
			}
			keys := map[string]bool{}
			for k := range ch.Before {
				keys[k] = true
			}
			for k := range ch.After {
				keys[k] = true
			}
			names := make([]string, 0, len(keys))
			for k := range keys {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				if reflect.DeepEqual(ch.Before[k], ch.After[k]) {
					continue
				}
				before, after := xraycfg.RawJSON(ch.Before[k]), xraycfg.RawJSON(ch.After[k])
				sb.WriteString(bot.Escape(k) + ": " + bot.Code(before) + " → " + bot.Code(after) + "\n")
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
