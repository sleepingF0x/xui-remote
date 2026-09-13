package routing

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/state"
	"github.com/lichenhao/xui-remote/internal/xraycfg"
)

// draft is the wizard's partial rule, carried between steps as a stashed token
// so no per-chat state has to be tracked separately.
type draft struct {
	Target string   `json:"t"`
	Kind   string   `json:"k"`
	Values []string `json:"v"`
}

func (d draft) encode() string {
	raw, _ := json.Marshal(d)
	return string(raw)
}

func decodeDraft(s string) (draft, error) {
	var d draft
	err := json.Unmarshal([]byte(s), &d)
	return d, err
}

func (m *Module) pickTarget(c *bot.Context) error {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return err
	}
	targets := xraycfg.Targets(tpl)
	if len(targets) == 0 {
		return fmt.Errorf("配置里没有任何出站，先去面板加一个")
	}
	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton
	for _, tag := range targets {
		token := c.Stash(draft{Target: tag}.encode())
		row = append(row, bot.Button(tag, c.CallbackData("nt", token)))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, bot.InlineRow(bot.BackTo(c, "返回列表", "list")))
	return c.Replace("<b>新增规则 1/3</b>\n\n流量指向哪个出站？", bot.Keyboard(rows...))
}

func (m *Module) pickKind(c *bot.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	raw, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新开始。")
	}
	d, err := decodeDraft(raw)
	if err != nil {
		return err
	}
	return c.Replace(
		fmt.Sprintf("<b>新增规则 2/3</b>\n\n出站：<b>%s</b>\n按什么匹配？", bot.Escape(d.Target)),
		bot.Keyboard(
			bot.InlineRow(
				bot.Button("域名 / 分类", c.CallbackData("nk", args[0], "domain")),
				bot.Button("IP / 网段", c.CallbackData("nk", args[0], "ip")),
			),
			bot.InlineRow(bot.BackTo(c, "返回列表", "list")),
		))
}

func (m *Module) askValue(c *bot.Context, args []string) error {
	if len(args) < 2 {
		return nil
	}
	raw, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新开始。")
	}
	d, err := decodeDraft(raw)
	if err != nil {
		return err
	}
	d.Kind = args[1]
	c.AwaitWith("value", d.encode())

	hint := "发送域名或分类，多个用空格或换行分隔：\n\n" +
		"<code>predict.fun</code>  后缀匹配，含子域名\n" +
		"<code>full:a.b.com</code>  精确匹配\n" +
		"<code>keyword:ads</code>  关键字\n" +
		"<code>geosite:cn</code>  官方分类\n" +
		"<code>ext:custom.dat:CRYPTO</code>  自定义分类"
	if d.Kind == "ip" {
		hint = "发送 IP、网段或 geoip 分类，多个用空格或换行分隔：\n\n" +
			"<code>1.1.1.1</code>\n<code>10.0.0.0/8</code>\n<code>geoip:cn</code>"
	}
	return c.Replace("<b>新增规则 3/3</b>\n\n"+hint,
		bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回列表", "list"))))
}

func (m *Module) askPosition(c *bot.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	raw, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新开始。")
	}
	d, err := decodeDraft(raw)
	if err != nil {
		return err
	}
	rules, err := m.rules()
	if err != nil {
		return err
	}
	c.AwaitWith("position", d.encode())
	return c.Replace(positionPrompt(rules, d),
		bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回列表", "list"))))
}

// positionPrompt renders the current ordering with the infrastructure prefix
// marked, then states the range the new rule may take.
func positionPrompt(rules []xraycfg.Rule, d draft) string {
	floor := xraycfg.SafeFloor(rules)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>插到第几位？</b>\n\n新规则：%s → <b>%s</b>\n\n<b>当前规则</b>\n",
		bot.Escape(strings.Join(d.Values, ", ")), bot.Escape(d.Target)))
	for _, r := range rules {
		mark := " "
		if r.Index < floor {
			mark = "🔒"
		} else if r.IsCatchAll() {
			mark = "⚠️"
		}
		sb.WriteString(fmt.Sprintf("%s %d. %s → <b>%s</b>\n",
			mark, r.Index+1, bot.Escape(truncateText(r.Summary(), 46)), bot.Escape(r.Target())))
	}
	if floor > 0 {
		sb.WriteString(fmt.Sprintf("\n🔒 前 %d 条是基础设施规则，顺序承重，不能插到它们中间。\n", floor))
	}
	sb.WriteString(fmt.Sprintf("\n发送一个序号 <b>%d</b> – <b>%d</b>（新规则占据该位置，原有规则顺延）：",
		floor+1, len(rules)+1))
	return sb.String()
}

func (m *Module) PromptWith(c *bot.Context, key, payload, text string) error {
	if key == "position" {
		return m.positionReply(c, payload, text)
	}
	if key == "moveto" {
		return m.moveToReply(c, payload, text)
	}
	if key == "select" {
		return m.selectReply(c, text)
	}
	if key == "filter" {
		if strings.TrimSpace(text) == "-" {
			return m.render(c, 0, "", false)
		}
		return m.render(c, 0, strings.TrimSpace(text), false)
	}
	if key != "value" {
		return nil
	}
	d, err := decodeDraft(payload)
	if err != nil {
		return err
	}
	values := normalizeValues(d.Kind, text)
	if len(values) == 0 {
		return fmt.Errorf("没解析出有效的匹配值")
	}
	d.Values = values

	rules, err := m.rules()
	if err != nil {
		return err
	}
	suggested := xraycfg.SuggestPosition(rules)
	floor := xraycfg.SafeFloor(rules)
	token := c.Stash(d.encode())

	var body strings.Builder
	body.WriteString(fmt.Sprintf("<b>规则预览</b>\n\n%s: %s\n→ <b>%s</b>\n\n插到第几位？",
		d.Kind, bot.Escape(strings.Join(values, ", ")), bot.Escape(d.Target)))
	if floor > 0 {
		body.WriteString(fmt.Sprintf("\n\n<i>前 %d 条是基础设施规则（api 路由、私有地址与协议封锁），"+
			"顺序承重，不能插到它们上面。</i>", floor))
	}

	suggestLabel := fmt.Sprintf("推荐：第 %d 位", suggested+1)
	if suggested < len(rules) {
		suggestLabel += "（catch-all 之前）"
	}
	return c.Send(body.String(), bot.Keyboard(
		bot.InlineRow(bot.Button(suggestLabel, c.CallbackData("np", token, strconv.Itoa(suggested)))),
		bot.InlineRow(
			bot.Button(fmt.Sprintf("最前（第 %d 位）", floor+1), c.CallbackData("np", token, strconv.Itoa(floor))),
			bot.Button("末尾", c.CallbackData("np", token, strconv.Itoa(len(rules)))),
		),
		bot.InlineRow(bot.Button("指定序号…", c.CallbackData("npick", token))),
		bot.InlineRow(bot.BackTo(c, "返回列表", "list")),
	))
}

// positionReply validates a typed position and stages the rule, re-asking
// rather than silently clamping so the operator sees why a choice was refused.
func (m *Module) positionReply(c *bot.Context, payload, text string) error {
	d, err := decodeDraft(payload)
	if err != nil {
		return err
	}
	rules, err := m.rules()
	if err != nil {
		return err
	}
	idx, err := validatePosition(rules, text)
	if err != nil {
		// Keep the prompt open so a typo does not drop the half-built rule.
		c.AwaitWith("position", payload)
		return err
	}
	return m.stage(c, d, idx)
}

// validatePosition turns a typed 1-based position into an index, refusing
// anything inside the infrastructure prefix instead of quietly clamping it.
// An insert may take the slot past the last rule; a move may not.
func validatePosition(rules []xraycfg.Rule, text string) (int, error) {
	return validatePositionIn(rules, text, true)
}

func validatePositionIn(rules []xraycfg.Rule, text string, insert bool) (int, error) {
	floor := xraycfg.SafeFloor(rules)
	hi := len(rules)
	if insert {
		hi++
	}
	lo := floor + 1

	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("%q 不是数字。发送 %d – %d 之间的序号", strings.TrimSpace(text), lo, hi)
	}
	if n < lo {
		if floor > 0 && n >= 1 {
			return 0, fmt.Errorf("第 %d 位在基础设施规则里，插进去会吃掉 api 入站的流量。请发送 %d – %d", n, lo, hi)
		}
		return 0, fmt.Errorf("序号从 %d 开始", lo)
	}
	if n > hi {
		return 0, fmt.Errorf("只有 %d 条规则，最大可填 %d", len(rules), hi)
	}
	return n - 1, nil
}

// selectReply turns a number typed at the rule list into a detail view. Text
// that is not a number re-offers the list rather than erroring, since the list
// leaves the prompt armed and a stray message should not look like a failure.
func (m *Module) selectReply(c *bot.Context, text string) error {
	rules, err := m.rules()
	if err != nil {
		return err
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(text))
	if convErr != nil || n < 1 || n > len(rules) {
		c.Await("select")
		return fmt.Errorf("发送 1 – %d 之间的序号来选中规则", len(rules))
	}
	return m.view(c, n-1)
}

func (m *Module) askMoveTo(c *bot.Context, idx int) error {
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	if floor := xraycfg.SafeFloor(rules); idx < floor {
		return fmt.Errorf("第 %d 条是基础设施规则，它的位置不该动", idx+1)
	}
	c.AwaitWith("moveto", strconv.Itoa(idx))
	return c.Replace(movePrompt(rules, idx),
		bot.Keyboard(bot.InlineRow(bot.Back(c, "view", strconv.Itoa(idx)))))
}

func movePrompt(rules []xraycfg.Rule, from int) string {
	floor := xraycfg.SafeFloor(rules)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>把第 %d 条移到哪里？</b>\n\n<b>当前规则</b>\n", from+1))
	for _, r := range rules {
		mark := " "
		switch {
		case r.Index == from:
			mark = "👉"
		case r.Index < floor:
			mark = "🔒"
		case r.IsCatchAll():
			mark = "⚠️"
		}
		sb.WriteString(fmt.Sprintf("%s %d. %s → <b>%s</b>\n",
			mark, r.Index+1, bot.Escape(truncateText(r.Summary(), 46)), bot.Escape(r.Target())))
	}
	if floor > 0 {
		sb.WriteString(fmt.Sprintf("\n🔒 前 %d 条顺序承重，不能挪到它们中间。\n", floor))
	}
	sb.WriteString(fmt.Sprintf("\n发送目标序号 <b>%d</b> – <b>%d</b>：", floor+1, len(rules)))
	return sb.String()
}

func (m *Module) moveToReply(c *bot.Context, payload, text string) error {
	from, err := strconv.Atoi(payload)
	if err != nil {
		return fmt.Errorf("规则序号丢失，请重新选择")
	}
	rules, err := m.rules()
	if err != nil {
		return err
	}
	to, err := validatePositionIn(rules, text, false)
	if err != nil {
		c.AwaitWith("moveto", payload)
		return err
	}
	if to == from {
		return c.Send("位置没变。")
	}
	return m.move(c, from, to)
}

func (m *Module) stageNew(c *bot.Context, args []string) error {
	if len(args) < 2 {
		return nil
	}
	raw, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新开始。")
	}
	d, err := decodeDraft(raw)
	if err != nil {
		return err
	}
	pos, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("位置 %q 不是数字", args[1])
	}
	return m.stage(c, d, pos)
}

func (m *Module) stage(c *bot.Context, d draft, pos int) error {
	field := "domain"
	if d.Kind == "ip" {
		field = "ip"
	}
	values := make([]any, len(d.Values))
	for i, v := range d.Values {
		values[i] = v
	}
	rule := xraycfg.Rule{Raw: map[string]any{
		"type":        "field",
		field:         values,
		"outboundTag": d.Target,
	}}

	current, err := m.rules()
	if err != nil {
		return err
	}
	// Clamp again at commit time: the button carries a position computed when
	// the preview was drawn, and the rule set may have moved since.
	pos = xraycfg.ClampPosition(current, pos)

	summary := fmt.Sprintf("新增规则 @%d (%s: %s → %s)",
		pos+1, field, strings.Join(d.Values, ", "), d.Target)
	err = m.txn.EditWithChecks("routing", summary, buildChecks(d, field, pos), func(tpl map[string]any) error {
		rules, err := xraycfg.Rules(tpl)
		if err != nil {
			return err
		}
		xraycfg.SetRules(tpl, xraycfg.Insert(rules, xraycfg.ClampPosition(rules, pos), rule))
		return nil
	})
	if err != nil {
		return err
	}
	return m.staged(c, summary)
}

// normalizeValues splits user input and makes every domain entry explicit. A
// bare string in an Xray routing rule is a substring match, far broader than
// the suffix match the same text means in a geosite source file.
func normalizeValues(kind, text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == '\r'
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if kind == "domain" && !hasRoutingPrefix(f) {
			f = "domain:" + strings.TrimPrefix(strings.ToLower(f), "www.")
		}
		out = append(out, f)
	}
	return out
}

func hasRoutingPrefix(s string) bool {
	for _, p := range []string{"domain:", "full:", "keyword:", "regexp:", "geosite:", "ext:"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			return true
		}
	}
	return false
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

// maxChecks bounds post-apply verification: a rule listing fifty domains does
// not need fifty route tests to show whether it took effect.
const maxChecks = 3

func buildChecks(d draft, field string, pos int) []state.Check {
	checks := make([]state.Check, 0, maxChecks)
	for _, v := range d.Values {
		if len(checks) == maxChecks {
			break
		}
		checks = append(checks, state.Check{
			Kind: field, Value: v, Expect: d.Target, Index: pos,
		})
	}
	return checks
}

func truncateText(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// pickNewTarget lists every outbound and balancer as a retarget option for an
// existing rule; only the target field changes, so the rule keeps its position.
func (m *Module) pickNewTarget(c *bot.Context, idx int) error {
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	tpl, err := m.txn.Inspect()
	if err != nil {
		return err
	}
	current := rules[idx].Target()

	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton
	for _, tag := range xraycfg.Targets(tpl) {
		label := tag
		if tag == current || tag+" (balancer)" == current {
			label = "• " + tag
		}
		row = append(row, bot.Button(label, c.CallbackData("chgset", strconv.Itoa(idx), c.Stash(tag))))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, bot.InlineRow(bot.Button("取消", c.CallbackData("view", strconv.Itoa(idx)))))

	return c.Replace(fmt.Sprintf("<b>规则 %d</b>\n%s\n\n当前出站 <b>%s</b>，改成哪个？",
		idx+1, bot.Escape(rules[idx].Summary()), bot.Escape(current)), bot.Keyboard(rows...))
}

func (m *Module) retarget(c *bot.Context, args []string) error {
	if len(args) < 2 {
		return nil
	}
	idx, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("规则序号无效")
	}
	tag, ok := c.Unstash(args[1])
	if !ok {
		return c.Replace("这个按钮已过期，请重新选择。")
	}
	rules, err := m.rules()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("规则 %d 不存在", idx+1)
	}
	tpl, err := m.txn.Inspect()
	if err != nil {
		return err
	}
	isBalancer := slices.Contains(xraycfg.BalancerTags(tpl), tag)
	before := rules[idx].Target()
	if before == tag {
		return c.Replace("出站没变。", bot.Keyboard(
			bot.InlineRow(bot.Button("← 返回", c.CallbackData("view", strconv.Itoa(idx))))))
	}

	field, values := rules[idx].MatchValues()
	summary := fmt.Sprintf("规则 %d 出站 %s → %s", idx+1, before, tag)
	err = m.txn.EditWithChecks("routing", summary, retargetChecks(rules[idx], field, values, tag, idx),
		func(tpl map[string]any) error {
			staged, err := xraycfg.Rules(tpl)
			if err != nil {
				return err
			}
			if idx >= len(staged) {
				return fmt.Errorf("规则 %d 不存在", idx+1)
			}
			staged[idx].SetTarget(tag, isBalancer)
			xraycfg.SetRules(tpl, staged)
			return nil
		})
	if err != nil {
		return err
	}
	return m.staged(c, summary)
}

func retargetChecks(r xraycfg.Rule, field string, values []string, tag string, idx int) []state.Check {
	if field == "" {
		return nil
	}
	user := firstUser(r)
	checks := make([]state.Check, 0, maxChecks)
	for _, v := range values {
		if len(checks) == maxChecks {
			break
		}
		checks = append(checks, state.Check{Kind: field, Value: v, Expect: tag, Index: idx, User: user})
	}
	return checks
}

// firstUser picks an identity the rule accepts; any one of them is enough to
// make the probe reach the rule at all.
func firstUser(r xraycfg.Rule) string {
	items, ok := r.Raw["user"].([]any)
	if !ok {
		return ""
	}
	for _, it := range items {
		if s, _ := it.(string); s != "" {
			return s
		}
	}
	return ""
}
