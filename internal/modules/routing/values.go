package routing

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/sleepingF0x/xui-remote/internal/bot"
	"github.com/sleepingF0x/xui-remote/internal/state"
	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
)

// values renders a rule's matchers with one removal button each. Every edit is
// staged, so a single tap is recoverable by discarding the staging area.
func (m *Module) values(c *bot.Context, idx int) error {
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	field, vals := rules[idx].MatchValues()
	if field == "" {
		return fmt.Errorf("第 %d 条不按域名或 IP 匹配，没有可编辑的匹配值", idx+1)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>规则 %d 的匹配值</b>（%s）\n→ <b>%s</b>\n\n",
		idx+1, field, bot.Escape(rules[idx].Target())))
	for i, v := range vals {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, bot.Code(v)))
	}
	if floor := xraycfg.SafeFloor(rules); idx < floor {
		sb.WriteString("\n🔒 <i>这是基础设施规则，改它的匹配值会影响 api 路由或私有地址封锁。</i>\n")
	}

	i := strconv.Itoa(idx)
	rows := [][]telego.InlineKeyboardButton{
		bot.InlineRow(bot.Button("+ 添加值", c.CallbackData("valadd", i))),
	}
	for vi, v := range vals {
		// Removing the last matcher would leave a rule that matches everything.
		if len(vals) == 1 {
			break
		}
		rows = append(rows, bot.InlineRow(
			bot.Button("✕ "+truncateText(v, 30), c.CallbackData("valdel", i, strconv.Itoa(vi)))))
	}
	if len(vals) == 1 {
		sb.WriteString("\n<i>只剩一个匹配值，删掉会让规则匹配所有流量 —— 要移除请删整条规则。</i>")
	}
	rows = append(rows, bot.InlineRow(bot.Back(c, "view", i)))
	return c.Replace(sb.String(), bot.Keyboard(rows...))
}

func (m *Module) askValueAdd(c *bot.Context, idx int) error {
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	field, _ := rules[idx].MatchValues()
	c.AwaitWith("valadd", strconv.Itoa(idx))

	hint := "发送要加入的域名或分类，多个用空格或换行分隔：\n\n" +
		"<code>predict.fun</code>  后缀匹配\n<code>full:a.b.com</code>  精确\n" +
		"<code>geosite:cn</code>  官方分类\n<code>ext:custom.dat:CRYPTO</code>  自定义分类"
	if field == "ip" {
		hint = "发送要加入的 IP、网段或 geoip 分类：\n\n<code>1.1.1.1</code>\n<code>10.0.0.0/8</code>\n<code>geoip:cn</code>"
	}
	return c.Replace(fmt.Sprintf("<b>给规则 %d 添加匹配值</b>\n\n%s", idx+1, hint),
		bot.Keyboard(bot.InlineRow(bot.Back(c, "vals", strconv.Itoa(idx)))))
}

func (m *Module) valueAddReply(c *bot.Context, payload, text string) error {
	idx, err := strconv.Atoi(payload)
	if err != nil {
		return fmt.Errorf("规则序号丢失，请重新选择")
	}
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	field, existing := rules[idx].MatchValues()
	if field == "" {
		return fmt.Errorf("第 %d 条不按域名或 IP 匹配", idx+1)
	}

	added := dedupe(existing, normalizeValues(field, text))
	if len(added) == 0 {
		c.AwaitWith("valadd", payload)
		return fmt.Errorf("没有新值可加（可能都已经在规则里了）")
	}

	target := rules[idx].Target()
	summary := fmt.Sprintf("规则 %d 添加 %s: %s", idx+1, field, strings.Join(added, ", "))
	err = m.txn.EditWithChecks("routing", summary, checksFor(added, field, target, idx, firstUser(rules[idx])),
		func(tpl map[string]any) error {
			staged, err := xraycfg.Rules(tpl)
			if err != nil {
				return err
			}
			if idx >= len(staged) {
				return fmt.Errorf("规则 %d 不存在", idx+1)
			}
			staged[idx].Raw[field] = toAny(append(existing, added...))
			xraycfg.SetRules(tpl, staged)
			return nil
		})
	if err != nil {
		return err
	}
	return m.staged(c, summary)
}

func (m *Module) deleteValue(c *bot.Context, args []string) error {
	idx, vi := atoi(args, 0), atoi(args, 1)
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	field, vals := rules[idx].MatchValues()
	if field == "" || vi < 0 || vi >= len(vals) {
		return fmt.Errorf("这个匹配值已经不在了")
	}
	if len(vals) == 1 {
		return fmt.Errorf("这是最后一个匹配值，删掉规则会匹配所有流量；要移除请删整条规则")
	}

	removed := vals[vi]
	summary := fmt.Sprintf("规则 %d 移除 %s: %s", idx+1, field, removed)
	err = m.txn.Edit("routing", summary, func(tpl map[string]any) error {
		staged, err := xraycfg.Rules(tpl)
		if err != nil {
			return err
		}
		if idx >= len(staged) {
			return fmt.Errorf("规则 %d 不存在", idx+1)
		}
		kept := append(append([]string{}, vals[:vi]...), vals[vi+1:]...)
		staged[idx].Raw[field] = toAny(kept)
		xraycfg.SetRules(tpl, staged)
		return nil
	})
	if err != nil {
		return err
	}
	return m.staged(c, summary)
}

// dedupe keeps only values the rule does not already carry, so re-adding one
// does not grow the list with duplicates.
func dedupe(existing, candidates []string) []string {
	have := make(map[string]bool, len(existing))
	for _, v := range existing {
		have[strings.ToLower(v)] = true
	}
	var out []string
	for _, v := range candidates {
		key := strings.ToLower(v)
		if have[key] {
			continue
		}
		have[key] = true
		out = append(out, v)
	}
	return out
}

func checksFor(values []string, field, target string, idx int, user string) []state.Check {
	checks := make([]state.Check, 0, maxChecks)
	for _, v := range values {
		if len(checks) == maxChecks {
			break
		}
		checks = append(checks, state.Check{Kind: field, Value: v, Expect: target, Index: idx, User: user})
	}
	return checks
}

func toAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
