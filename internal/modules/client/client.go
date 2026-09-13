// Package client looks up clients and hands out their subscription and share
// links. Everything here is read-only and takes effect immediately, so it does
// not touch the config staging area.
package client

import (
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/detect"
	"github.com/lichenhao/xui-remote/internal/panel"
)

const maxSearchResults = 12

type Module struct {
	panel *panel.Client
}

func New(p *panel.Client) *Module { return &Module{panel: p} }

func (m *Module) Name() string  { return "client" }
func (m *Module) Title() string { return "👥 客户端" }

func (m *Module) Commands() []bot.Command {
	return []bot.Command{{
		Name:        "client",
		Description: "查找客户端并取订阅链接",
		Handler: func(c *bot.Context, args string) error {
			if args == "" {
				return m.Menu(c)
			}
			return m.search(c, args)
		},
	}}
}

func (m *Module) Menu(c *bot.Context) error {
	c.Await("search")
	return c.Send("发送客户端标识（邮箱或名称，支持部分匹配）：",
		bot.Keyboard(bot.InlineRow(bot.Home())))
}

func (m *Module) Prompt(c *bot.Context, key, text string) error {
	if key != "search" {
		return nil
	}
	return m.search(c, text)
}

// MatchText offers a client lookup for an email-shaped input, which is what
// x-ui uses as the client identifier.
func (m *Module) MatchText(input string) []bot.TextOffer {
	if detect.Classify(input).Kind != detect.KindEmail {
		return nil
	}
	return []bot.TextOffer{{
		Label:    "查看客户端",
		Callback: bot.CallbackData("client", "view", inlineRef(strings.TrimSpace(input))),
	}}
}

func (m *Module) Callback(c *bot.Context, action string, args []string) error {
	email, ok := resolve(c, args)
	if !ok {
		return c.Replace("这个按钮已过期，请重新查找客户端。")
	}
	switch action {
	case "view":
		return m.view(c, email, true)
	case "sub":
		return m.subscription(c, email)
	case "links":
		return m.shareLinks(c, email)
	case "qr":
		return m.qr(c, email)
	case "traffic":
		return m.traffic(c, email)
	}
	return nil
}

// An identifier reaches a callback either stashed — it can outgrow Telegram's
// 64-byte limit — or inline, from the text dispatcher, which has no Context to
// stash with. The kind is marked so a stash miss is a stale button rather than
// the token itself being mistaken for the identifier.
const (
	refStashed = "s"
	refInline  = "r"
)

func stashedRef(c *bot.Context, email string) string { return refStashed + ":" + c.Stash(email) }

func inlineRef(email string) string { return refInline + ":" + email }

// parseRef splits a reference into its kind and value without resolving it, so
// the classification can be checked on its own.
func parseRef(args []string) (kind, value string, ok bool) {
	if len(args) < 2 {
		return "", "", false
	}
	kind = args[0]
	if kind != refStashed && kind != refInline {
		return "", "", false
	}
	// The value is rejoined: an address may itself contain a colon.
	return kind, strings.Join(args[1:], ":"), true
}

func resolve(c *bot.Context, args []string) (string, bool) {
	kind, value, ok := parseRef(args)
	if !ok {
		return "", false
	}
	if kind == refInline {
		return value, value != ""
	}
	return c.Unstash(value)
}

func (m *Module) search(c *bot.Context, query string) error {
	page, err := m.panel.SearchClients(strings.TrimSpace(query), 1, maxSearchResults)
	if err != nil {
		return fmt.Errorf("查找失败: %w", err)
	}
	if len(page.Items) == 0 {
		return c.Send(fmt.Sprintf("没有匹配 <code>%s</code> 的客户端。", bot.Escape(query)))
	}
	if len(page.Items) == 1 {
		return m.view(c, page.Items[0].Email, false)
	}

	var rows [][]telego.InlineKeyboardButton
	for _, it := range page.Items {
		rows = append(rows, bot.InlineRow(
			bot.Button(statusMark(it)+" "+it.Email, c.CallbackData("view", stashedRef(c, it.Email)))))
	}
	rows = append(rows, bot.InlineRow(bot.Home()))
	header := fmt.Sprintf("匹配到 <b>%d</b> 个（共 %d）：", len(page.Items), page.Filtered)
	return c.Send(header, bot.Keyboard(rows...))
}

func (m *Module) view(c *bot.Context, email string, replace bool) error {
	if email == "" {
		return fmt.Errorf("没有指定客户端")
	}
	t, err := m.panel.ClientTraffic(email)
	if err != nil {
		return fmt.Errorf("读取客户端失败: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>👤 %s</b>\n\n", bot.Escape(email)))
	sb.WriteString("状态    " + enabledText(t.Enable) + "\n")
	sb.WriteString("流量    " + trafficText(t) + "\n")
	sb.WriteString("到期    " + expiryText(t.ExpiryTime) + "\n")
	if t.LastOnline > 0 {
		sb.WriteString("最后在线 " + humanMillis(t.LastOnline) + "\n")
	}

	token := stashedRef(c, email)
	kb := bot.Keyboard(
		bot.InlineRow(
			bot.Button("订阅链接", c.CallbackData("sub", token)),
			bot.Button("单条链接", c.CallbackData("links", token)),
		),
		bot.InlineRow(
			bot.Button("二维码", c.CallbackData("qr", token)),
			bot.Button("刷新", c.CallbackData("view", token)),
		),
		bot.InlineRow(bot.Home()),
	)
	if replace {
		return c.Replace(sb.String(), kb)
	}
	return c.Send(sb.String(), kb)
}

func (m *Module) subscription(c *bot.Context, email string) error {
	subURL, err := m.subURL(email)
	if err != nil {
		return err
	}
	return c.Send("<b>订阅链接</b>\n"+bot.Code(subURL), backToClient(c, email))
}

func (m *Module) shareLinks(c *bot.Context, email string) error {
	subID, err := m.subID(email)
	if err != nil {
		return err
	}
	links, err := m.panel.SubLinks(subID)
	if err != nil {
		return fmt.Errorf("读取链接失败: %w", err)
	}
	if len(links) == 0 {
		return c.Send("这个客户端没有可用的分享链接。")
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>单条链接 — %s</b>\n\n", bot.Escape(email)))
	for _, l := range links {
		sb.WriteString(bot.Code(l) + "\n\n")
	}
	return c.Send(sb.String(), backToClient(c, email))
}

func (m *Module) qr(c *bot.Context, email string) error {
	subURL, err := m.subURL(email)
	if err != nil {
		return err
	}
	png, err := qrcode.Encode(subURL, qrcode.Medium, 512)
	if err != nil {
		return fmt.Errorf("生成二维码失败: %w", err)
	}
	return c.SendPhoto("sub.png", png, "订阅二维码 — "+bot.Escape(email), backToClient(c, email))
}

func (m *Module) traffic(c *bot.Context, email string) error {
	t, err := m.panel.ClientTraffic(email)
	if err != nil {
		return err
	}
	return c.Send(fmt.Sprintf("<b>%s</b>\n上行 %s · 下行 %s\n%s",
		bot.Escape(email), humanBytes(t.Up), humanBytes(t.Down), trafficText(t)),
		backToClient(c, email))
}

// subURL joins the panel's configured subscription base with the client's
// subId. The base comes from the panel so a reverse-proxied setup renders the
// same URL the panel's own pages do.
func (m *Module) subID(email string) (string, error) {
	page, err := m.panel.SearchClients(email, 1, 5)
	if err != nil {
		return "", fmt.Errorf("读取客户端失败: %w", err)
	}
	for _, it := range page.Items {
		if it.Email == email {
			if it.SubID == "" {
				return "", fmt.Errorf("客户端 %s 没有 subId", email)
			}
			return it.SubID, nil
		}
	}
	return "", fmt.Errorf("找不到客户端 %s", email)
}

func (m *Module) subURL(email string) (string, error) {
	subID, err := m.subID(email)
	if err != nil {
		return "", err
	}
	settings, err := m.panel.PanelSettings()
	if err != nil {
		return "", fmt.Errorf("读取面板订阅设置失败: %w", err)
	}
	if !settings.SubEnable {
		return "", fmt.Errorf("面板的订阅服务未启用")
	}
	if settings.SubURI == "" {
		return "", fmt.Errorf("面板没有配置订阅地址（subURI），生成出来的链接客户端访问不到")
	}
	return strings.TrimSuffix(settings.SubURI, "/") + "/" + subID, nil
}

// backToClient returns to the client's own screen rather than the search that
// found it — the search text is not carried, and a dead end is worse.
func backToClient(c *bot.Context, email string) *telego.InlineKeyboardMarkup {
	return bot.Keyboard(bot.InlineRow(bot.Back(c, "view", stashedRef(c, email))))
}

func statusMark(it panel.ClientSlim) string {
	if !it.Enable {
		return "⏸"
	}
	return "✅"
}

func enabledText(enabled bool) string {
	if enabled {
		return "✅ 启用"
	}
	return "⛔️ 停用"
}

func trafficText(t *panel.ClientTraffic) string {
	used := t.Up + t.Down
	if t.Total <= 0 {
		return humanBytes(used) + " / 不限"
	}
	pct := float64(used) / float64(t.Total) * 100
	return fmt.Sprintf("%s / %s (%.0f%%)", humanBytes(used), humanBytes(t.Total), pct)
}

func expiryText(millis int64) string {
	if millis == 0 {
		return "永不过期"
	}
	t := time.UnixMilli(millis)
	left := time.Until(t)
	if left <= 0 {
		return t.Format("2006-01-02") + "（已过期）"
	}
	return fmt.Sprintf("%s（剩 %d 天）", t.Format("2006-01-02"), int(left.Hours()/24))
}

func humanMillis(millis int64) string {
	d := time.Since(time.UnixMilli(millis))
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

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
