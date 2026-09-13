package routing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
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
	rows = append(rows, bot.InlineRow(bot.Button("取消", "core:cancel")))
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
			bot.InlineRow(bot.Button("取消", "core:cancel")),
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
	return c.Replace("<b>新增规则 3/3</b>\n\n" + hint)
}

func (m *Module) PromptWith(c *bot.Context, key, payload, text string) error {
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
	token := c.Stash(d.encode())

	body := fmt.Sprintf("<b>规则预览</b>\n\n%s: %s\n→ <b>%s</b>\n\n插到第几位？",
		d.Kind, bot.Escape(strings.Join(values, ", ")), bot.Escape(d.Target))

	suggestLabel := fmt.Sprintf("推荐：第 %d 位", suggested+1)
	if suggested < len(rules) {
		suggestLabel += "（catch-all 之前）"
	}
	return c.Send(body, bot.Keyboard(
		bot.InlineRow(bot.Button(suggestLabel, c.CallbackData("np", token, strconv.Itoa(suggested)))),
		bot.InlineRow(
			bot.Button("置顶", c.CallbackData("np", token, "0")),
			bot.Button("末尾", c.CallbackData("np", token, strconv.Itoa(len(rules)))),
		),
		bot.InlineRow(bot.Button("取消", "core:cancel")),
	))
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

	summary := fmt.Sprintf("新增规则 @%d (%s: %s → %s)",
		pos+1, field, strings.Join(d.Values, ", "), d.Target)
	err = m.txn.Edit("routing", summary, func(tpl map[string]any) error {
		rules, err := xraycfg.Rules(tpl)
		if err != nil {
			return err
		}
		xraycfg.SetRules(tpl, xraycfg.Insert(rules, pos, rule))
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
