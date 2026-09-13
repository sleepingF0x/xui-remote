// Package routing edits routing.rules. Every edit is staged; nothing reaches
// the panel until the config module applies it.
package routing

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/txn"
	"github.com/lichenhao/xui-remote/internal/xraycfg"
)

type Module struct {
	txn *txn.Manager
}

func New(t *txn.Manager) *Module { return &Module{txn: t} }

func (m *Module) Name() string  { return "route" }
func (m *Module) Title() string { return "🔀 路由" }

func (m *Module) Commands() []bot.Command {
	return []bot.Command{{
		Name:        "rules",
		Description: "列出路由规则",
		Handler:     func(c *bot.Context, _ string) error { return m.list(c, false) },
	}}
}

func (m *Module) Menu(c *bot.Context) error { return m.list(c, false) }

func (m *Module) Callback(c *bot.Context, action string, args []string) error {
	switch action {
	case "list":
		return m.list(c, true)
	case "view":
		return m.view(c, atoi(args, 0))
	case "del":
		return m.confirmDelete(c, atoi(args, 0))
	case "delgo":
		return m.deleteRule(c, atoi(args, 0))
	case "up":
		return m.move(c, atoi(args, 0), atoi(args, 0)-1)
	case "down":
		return m.move(c, atoi(args, 0), atoi(args, 0)+1)
	case "new":
		return m.pickTarget(c)
	case "nt":
		return m.pickKind(c, args)
	case "nk":
		return m.askValue(c, args)
	case "np":
		return m.stageNew(c, args)
	}
	return nil
}

func (m *Module) list(c *bot.Context, replace bool) error {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return err
	}
	rules, err := xraycfg.Rules(tpl)
	if err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString("<b>🔀 路由规则</b>\n\n")
	if len(rules) == 0 {
		sb.WriteString("<i>还没有任何规则。</i>\n")
	}
	shadowed := false
	for _, r := range rules {
		marker := " "
		if r.IsCatchAll() {
			marker = "⚠️"
		} else if shadowed {
			marker = "🚫"
		}
		sb.WriteString(fmt.Sprintf("%s <b>%d.</b> %s → <b>%s</b>\n",
			marker, r.Index+1, bot.Escape(r.Summary()), bot.Escape(r.Target())))
		if r.IsCatchAll() {
			sb.WriteString("      <i>catch-all，之后的规则不会生效</i>\n")
			shadowed = true
		}
	}
	if shadowed {
		sb.WriteString("\n🚫 = 被前面的 catch-all 遮蔽")
	}

	kb := buildListKeyboard(c, len(rules))
	if replace {
		return c.Replace(sb.String(), kb)
	}
	return c.Send(sb.String(), kb)
}

func buildListKeyboard(c *bot.Context, count int) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton
	for i := 0; i < count; i++ {
		row = append(row, bot.Button(strconv.Itoa(i+1), c.CallbackData("view", strconv.Itoa(i))))
		if len(row) == 6 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, bot.InlineRow(bot.Button("+ 新增规则", c.CallbackData("new"))))
	return bot.Keyboard(rows...)
}

func (m *Module) view(c *bot.Context, idx int) error {
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	r := rules[idx]
	body := fmt.Sprintf("<b>规则 %d</b> → <b>%s</b>\n\n%s",
		idx+1, bot.Escape(r.Target()), bot.Code(xraycfg.RawJSON(r.Raw)))

	i := strconv.Itoa(idx)
	kb := bot.Keyboard(
		bot.InlineRow(
			bot.Button("↑ 上移", c.CallbackData("up", i)),
			bot.Button("↓ 下移", c.CallbackData("down", i)),
			bot.Button("删除", c.CallbackData("del", i)),
		),
		bot.InlineRow(bot.Button("← 返回列表", c.CallbackData("list"))),
	)
	return c.Replace(body, kb)
}

func (m *Module) confirmDelete(c *bot.Context, idx int) error {
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	r := rules[idx]
	return c.Replace(
		fmt.Sprintf("删除规则 %d？\n\n%s → <b>%s</b>", idx+1, bot.Escape(r.Summary()), bot.Escape(r.Target())),
		bot.Keyboard(
			bot.InlineRow(bot.Button("确认删除", c.CallbackData("delgo", strconv.Itoa(idx)))),
			bot.InlineRow(bot.Button("取消", c.CallbackData("view", strconv.Itoa(idx)))),
		))
}

func (m *Module) deleteRule(c *bot.Context, idx int) error {
	current, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(current) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	summary := fmt.Sprintf("删除规则 %d (%s → %s)", idx+1, current[idx].Summary(), current[idx].Target())

	err = m.txn.Edit("routing", summary, func(tpl map[string]any) error {
		rules, err := xraycfg.Rules(tpl)
		if err != nil {
			return err
		}
		next, err := xraycfg.Remove(rules, idx)
		if err != nil {
			return err
		}
		xraycfg.SetRules(tpl, next)
		return nil
	})
	if err != nil {
		return err
	}
	return m.staged(c, summary)
}

func (m *Module) move(c *bot.Context, from, to int) error {
	current, err := m.rules()
	if err != nil {
		return err
	}
	// Moving a business rule above the infrastructure prefix can swallow the
	// api inbound's own traffic, which takes the panel's gRPC API down.
	if floor := xraycfg.SafeFloor(current); to < floor && from >= floor {
		return c.Replace(
			fmt.Sprintf("不能再往上了。\n\n<i>前 %d 条是基础设施规则，顺序承重 —— "+
				"业务规则排到它们上面会吃掉 api 入站的流量，面板的统计和路由测试都会失效。</i>", floor),
			bot.Keyboard(bot.InlineRow(bot.Button("← 返回列表", c.CallbackData("list")))))
	}
	if to < 0 {
		return c.Replace("已经是第一条了。", bot.Keyboard(
			bot.InlineRow(bot.Button("← 返回列表", c.CallbackData("list")))))
	}
	summary := fmt.Sprintf("规则 %d 移到第 %d 位", from+1, to+1)
	err = m.txn.Edit("routing", summary, func(tpl map[string]any) error {
		rules, err := xraycfg.Rules(tpl)
		if err != nil {
			return err
		}
		if to >= len(rules) {
			return fmt.Errorf("已经是最后一条了")
		}
		next, err := xraycfg.Move(rules, from, to)
		if err != nil {
			return err
		}
		xraycfg.SetRules(tpl, next)
		return nil
	})
	if err != nil {
		return err
	}
	return m.list(c, true)
}

func (m *Module) rules() ([]xraycfg.Rule, error) {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return nil, err
	}
	return xraycfg.Rules(tpl)
}

func (m *Module) staged(c *bot.Context, summary string) error {
	return c.Replace("✅ 已暂存："+bot.Escape(summary)+"\n\n<i>还未生效，到「⚙️ 配置」提交。</i>",
		bot.Keyboard(
			bot.InlineRow(bot.Button("查看暂存区", "config:diff")),
			bot.InlineRow(bot.Button("← 返回列表", c.CallbackData("list"))),
		))
}
