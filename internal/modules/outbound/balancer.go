package outbound

import (
	"fmt"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/lichenhao/xui-remote/internal/bot"
)

// balancerTags reads routing.balancers from the template.
func (m *Module) balancerTags() ([]string, error) {
	tpl, err := m.txn.Inspect()
	if err != nil {
		return nil, err
	}
	routing, ok := tpl["routing"].(map[string]any)
	if !ok {
		return nil, nil
	}
	list, ok := routing["balancers"].([]any)
	if !ok {
		return nil, nil
	}
	var tags []string
	for _, b := range list {
		if mm, ok := b.(map[string]any); ok {
			if tag, _ := mm["tag"].(string); tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	return tags, nil
}

func (m *Module) balancers(c *bot.Context) error {
	tags, err := m.balancerTags()
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return c.Replace("配置里没有负载均衡器。", bot.Keyboard(
			bot.InlineRow(bot.Button("← 返回", c.CallbackData("menu")))))
	}
	var rows [][]telego.InlineKeyboardButton
	for _, tag := range tags {
		rows = append(rows, bot.InlineRow(bot.Button(tag, c.CallbackData("balpick", c.Stash(tag)))))
	}
	rows = append(rows, bot.InlineRow(bot.Button("← 返回", c.CallbackData("menu"))))
	return c.Replace(
		"<b>⚖️ 负载均衡</b>\n\n临时把某个均衡器钉到固定出站。<i>只影响运行时，不写进配置，重启后失效。</i>\n\n选一个均衡器：",
		bot.Keyboard(rows...))
}

func (m *Module) balancerTargets(c *bot.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	tag, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新选择。")
	}
	obs, err := m.outbounds()
	if err != nil {
		return err
	}
	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton
	for _, ob := range obs {
		if ob.Tag == "" || probeSkipProtocols[ob.Protocol] {
			continue
		}
		row = append(row, bot.Button(ob.Tag, c.CallbackData("balset", c.Stash(tag+"\x1f"+ob.Tag))))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows,
		bot.InlineRow(bot.Button("清除覆盖（恢复自动选择）", c.CallbackData("balset", c.Stash(tag+"\x1f")))),
		bot.InlineRow(bot.Button("← 返回", c.CallbackData("bal"))),
	)
	return c.Replace(fmt.Sprintf("<b>%s</b> 钉到哪个出站？", bot.Escape(tag)), bot.Keyboard(rows...))
}

func (m *Module) setBalancer(c *bot.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	payload, ok := c.Unstash(args[0])
	if !ok {
		return c.Replace("这个按钮已过期，请重新选择。")
	}
	tag, target, _ := strings.Cut(payload, "\x1f")
	if err := m.panel.OverrideBalancer(tag, target); err != nil {
		return fmt.Errorf("设置失败: %w", err)
	}
	back := bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回负载均衡", "bal")))
	if target == "" {
		return c.Replace(fmt.Sprintf("✅ <b>%s</b> 已恢复自动选择。", bot.Escape(tag)), back)
	}
	return c.Replace(fmt.Sprintf("✅ <b>%s</b> 已钉到 <b>%s</b>。\n\n<i>Xray 重启后失效。</i>",
		bot.Escape(tag), bot.Escape(target)), back)
}
