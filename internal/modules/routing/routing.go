// Package routing edits routing.rules. Every edit is staged; nothing reaches
// the panel until the config module applies it.
package routing

import (
	"fmt"
	"reflect"
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
		return m.listPage(c, args, true)
	case "filter":
		c.Await("filter")
		return c.Replace("发送出站名或域名片段筛选，发送 <code>-</code> 清除筛选：")
	case "view":
		return m.view(c, atoi(args, 0))
	case "del":
		return m.confirmDelete(c, atoi(args, 0))
	case "delgo":
		return m.deleteRule(c, atoi(args, 0))
	case "chg":
		return m.pickNewTarget(c, atoi(args, 0))
	case "chgset":
		return m.retarget(c, args)
	case "moveto":
		return m.askMoveTo(c, atoi(args, 0))
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
	case "npick":
		return m.askPosition(c, args)
	case "np":
		return m.stageNew(c, args)
	}
	return nil
}

// pageSize keeps one screenful readable; a per-rule button grid was dropped
// because the number in it carries no information — you have to read the list,
// find the line, then hunt the same number in the grid.
const pageSize = 12

func (m *Module) list(c *bot.Context, replace bool) error {
	return m.render(c, 0, "", replace)
}

func (m *Module) listPage(c *bot.Context, args []string, replace bool) error {
	page := 0
	if len(args) > 0 {
		page = atoi(args, 0)
	}
	filter := ""
	if len(args) > 1 {
		if v, ok := c.Unstash(args[1]); ok {
			filter = v
		}
	}
	return m.render(c, page, filter, replace)
}

func (m *Module) render(c *bot.Context, page int, filter string, replace bool) error {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return err
	}
	rules, err := xraycfg.Rules(tpl)
	if err != nil {
		return err
	}
	shown := matching(rules, filter)

	if page < 0 {
		page = 0
	}
	pages := max(1, (len(shown)+pageSize-1)/pageSize)
	if page >= pages {
		page = pages - 1
	}
	start := page * pageSize
	end := min(start+pageSize, len(shown))

	pending := m.pendingRules(rules)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>🔀 路由规则</b> (%d)", len(rules)))
	if filter != "" {
		sb.WriteString(fmt.Sprintf("  <i>筛选 “%s” — %d 条</i>", bot.Escape(filter), len(shown)))
	}
	sb.WriteString("\n\n")
	switch {
	case len(rules) == 0:
		sb.WriteString("<i>还没有任何规则。</i>\n")
	case len(shown) == 0:
		sb.WriteString("<i>没有匹配的规则。</i>\n")
	}
	floor := xraycfg.SafeFloor(rules)
	for _, r := range shown[start:end] {
		sb.WriteString(ruleLine(r, floor, pending[r.Index]))
	}
	if n := m.txn.PendingCount(); n > 0 {
		sb.WriteString(fmt.Sprintf(
			"\n✏️ <b>%d 项改动尚未提交</b>，标 ✏️ 的规则是草稿，还没生效。\n"+
				"<i>路由测试打的是线上配置，这两处会对不上，直到提交。</i>\n", n))
	}
	if pages > 1 {
		sb.WriteString(fmt.Sprintf("\n第 %d / %d 页\n", page+1, pages))
	}
	if len(shown) > 0 {
		sb.WriteString("\n<i>发送序号查看或编辑某条</i>")
	}

	c.Await("select")
	return send(c, replace, sb.String(), m.listKeyboard(c, page, pages, filter))
}

func ruleLine(r xraycfg.Rule, floor int, draft bool) string {
	marker := "  "
	switch {
	case draft:
		marker = "✏️"
	case r.Index < floor:
		marker = "🔒"
	case r.IsCatchAll():
		marker = "⚠️"
	}
	line := fmt.Sprintf("%s <b>%d.</b> %s → <b>%s</b>\n",
		marker, r.Index+1, bot.Escape(truncateText(r.Summary(), 44)), bot.Escape(r.Target()))
	if r.IsCatchAll() {
		line += "      <i>catch-all，之后的规则不会生效</i>\n"
	}
	return line
}

// pendingRules marks the rules whose staged form differs from what is live, so
// the list cannot be read as the running config when it is not.
func (m *Module) pendingRules(draft []xraycfg.Rule) map[int]bool {
	base, ok := m.txn.BaseTemplate()
	if !ok {
		return nil
	}
	liveRules, err := xraycfg.Rules(base)
	if err != nil {
		return nil
	}
	out := map[int]bool{}
	for _, r := range draft {
		if r.Index >= len(liveRules) || !sameRule(liveRules[r.Index], r) {
			out[r.Index] = true
		}
	}
	return out
}

func sameRule(a, b xraycfg.Rule) bool {
	return reflect.DeepEqual(a.Raw, b.Raw)
}

// matching filters on the rendered summary and target, which is what the user
// is looking at — searching the raw JSON would match fields they cannot see.
func matching(rules []xraycfg.Rule, filter string) []xraycfg.Rule {
	if filter == "" {
		return rules
	}
	needle := strings.ToLower(filter)
	var out []xraycfg.Rule
	for _, r := range rules {
		if strings.Contains(strings.ToLower(r.Summary()+" "+r.Target()), needle) {
			out = append(out, r)
		}
	}
	return out
}

func (m *Module) listKeyboard(c *bot.Context, page, pages int, filter string) *telego.InlineKeyboardMarkup {
	token := c.Stash(filter)
	var rows [][]telego.InlineKeyboardButton
	if pages > 1 {
		var nav []telego.InlineKeyboardButton
		if page > 0 {
			nav = append(nav, bot.Button("← 上一页", c.CallbackData("list", strconv.Itoa(page-1), token)))
		}
		if page < pages-1 {
			nav = append(nav, bot.Button("下一页 →", c.CallbackData("list", strconv.Itoa(page+1), token)))
		}
		rows = append(rows, nav)
	}
	filterLabel := "🔍 筛选"
	if filter != "" {
		filterLabel = "🔍 清除筛选"
	}
	rows = append(rows, []telego.InlineKeyboardButton{
		bot.Button("+ 新增规则", c.CallbackData("new")),
		bot.Button(filterLabel, c.CallbackData("filter")),
	}, bot.InlineRow(bot.Home()))
	return bot.Keyboard(rows...)
}

func send(c *bot.Context, replace bool, text string, kb *telego.InlineKeyboardMarkup) error {
	if replace {
		return c.Replace(text, kb)
	}
	return c.Send(text, kb)
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
			bot.Button("移到…", c.CallbackData("moveto", i)),
		),
		bot.InlineRow(
			bot.Button("改出站", c.CallbackData("chg", i)),
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
