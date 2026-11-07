package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mymmrac/telego"

	"github.com/sleepingF0x/xui-remote/internal/bot"
	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/state"
)

// Client writes take effect on the panel immediately; none of them pass
// through the config staging area, which only covers the Xray template. The
// one irreversible step, deletion, saves what an undo needs first.

const gib = int64(1) << 30

var (
	quotaPresets  = []int{50, 100, 200}
	expiryPresets = []int{30, 90, 365}
)

// newDraft is the new-client wizard's state. It travels stashed in the
// buttons, one immutable copy per step, so an old step's button replays that
// step instead of corrupting a newer one.
type newDraft struct {
	Email    string `json:"e"`
	Inbounds []int  `json:"i"`
	// QuotaGB and Days are -1 until chosen; 0 means unlimited.
	QuotaGB int `json:"q"`
	Days    int `json:"d"`
}

func (d newDraft) stash(c *bot.Context) string {
	raw, _ := json.Marshal(d)
	return c.Stash(string(raw))
}

func loadDraft(c *bot.Context, token string) (newDraft, bool) {
	raw, ok := c.Unstash(token)
	if !ok {
		return newDraft{}, false
	}
	var d newDraft
	return d, json.Unmarshal([]byte(raw), &d) == nil
}

// manageCallback handles the write actions. It reports false for an action it
// does not own.
func (m *Module) manageCallback(c *bot.Context, action string, args []string) (bool, error) {
	switch action {
	case "new":
		c.AwaitWith("newemail", "")
		return true, c.Replace("<b>新增客户端</b>\n\n发送新客户端的标识（面板里的 email 字段），例如 <code>alice</code>。不能包含空格和斜杠。",
			bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回列表", "list"))))
	case "ntog", "nnext", "nq", "nd", "ngo", "nqask", "ndask":
		return true, m.wizard(c, action, args)
	}

	// The remaining actions name a client; those with a value carry it first,
	// because the client reference may itself contain colons.
	var value string
	switch action {
	case "expset", "quotaset":
		if len(args) < 1 {
			return true, nil
		}
		value, args = args[0], args[1:]
	case "exp", "expask", "quota", "quotaask", "reset", "resetgo", "toggle", "del", "delgo", "undo":
	default:
		return false, nil
	}
	email, ok := resolve(c, args)
	if !ok {
		return true, c.Replace("这个按钮已过期，请重新查找客户端。")
	}
	n, _ := strconv.Atoi(value)
	switch action {
	case "exp":
		return true, m.expiryMenu(c, email)
	case "expask":
		c.AwaitWith("expdays", email)
		return true, c.Replace("发送从今天起算的天数，例如 <code>45</code>：", backToClient(c, email))
	case "expset":
		return true, m.setExpiry(c, email, n)
	case "quota":
		return true, m.quotaMenu(c, email)
	case "quotaask":
		c.AwaitWith("quotagb", email)
		return true, c.Replace("发送新的流量额度（GB），例如 <code>300</code>；发送 <code>0</code> 表示不限：", backToClient(c, email))
	case "quotaset":
		return true, m.setQuota(c, email, n)
	case "reset":
		return true, c.Replace(fmt.Sprintf("确认把 <b>%s</b> 的已用流量清零？", bot.Escape(email)), bot.Keyboard(
			bot.InlineRow(bot.Button("确认清零", c.CallbackData("resetgo", stashedRef(c, email)))),
			bot.InlineRow(bot.Back(c, "view", stashedRef(c, email))),
		))
	case "resetgo":
		if err := m.panel.ResetClientTraffic(email); err != nil {
			return true, fmt.Errorf("重置流量失败: %w", err)
		}
		return true, m.viewWithNote(c, email, "已清零已用流量。")
	case "toggle":
		return true, m.toggle(c, email)
	case "del":
		return true, c.Replace(fmt.Sprintf("确认删除 <b>%s</b>？\n\n它的订阅链接和分享链接会立即失效。删除后可以撤销：撤销会用原来的凭据重建，链接恢复可用，但已用流量清零。",
			bot.Escape(email)), bot.Keyboard(
			bot.InlineRow(bot.Button("🗑 确认删除", c.CallbackData("delgo", stashedRef(c, email)))),
			bot.InlineRow(bot.Back(c, "view", stashedRef(c, email))),
		))
	case "delgo":
		return true, m.delete(c, email)
	case "undo":
		return true, m.undoDelete(c, email)
	}
	return false, nil
}

// PromptWith answers the questions the module asks, including the search box.
func (m *Module) PromptWith(c *bot.Context, key, payload, text string) error {
	text = strings.TrimSpace(text)
	switch key {
	case "search":
		return m.Prompt(c, key, text)
	case "newemail":
		return m.wizardEmail(c, text)
	case "newquota", "newdays":
		d, ok := loadDraft(c, payload)
		if !ok {
			return c.Send("这一步已过期，请重新开始新增客户端。")
		}
		n, err := positiveNumber(text)
		if err != nil {
			c.AwaitWith(key, payload)
			return c.Send(err.Error() + "，请重新发送：")
		}
		if key == "newquota" {
			d.QuotaGB = n
			return m.wizardDays(c, d, false)
		}
		d.Days = n
		return m.wizardConfirm(c, d, false)
	case "expdays":
		n, err := positiveNumber(text)
		if err != nil || n == 0 {
			c.AwaitWith(key, payload)
			return c.Send("需要一个大于 0 的整数天数，请重新发送：")
		}
		return m.setExpiryFromToday(c, payload, n)
	case "quotagb":
		n, err := positiveNumber(text)
		if err != nil {
			c.AwaitWith(key, payload)
			return c.Send(err.Error() + "，请重新发送：")
		}
		return m.setQuota(c, payload, n)
	}
	return nil
}

func positiveNumber(text string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(text)), "GB")))
	if err != nil || n < 0 {
		return 0, errors.New("需要一个不小于 0 的整数")
	}
	return n, nil
}

// validEmail mirrors the panel's own rule so the operator hears about a bad
// identifier before choosing everything else.
func validEmail(email string) error {
	if email == "" {
		return errors.New("标识不能为空")
	}
	for _, r := range email {
		if r == '/' || r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("标识不能包含空格、斜杠或控制字符")
		}
	}
	return nil
}

func (m *Module) wizardEmail(c *bot.Context, email string) error {
	if err := validEmail(email); err != nil {
		c.AwaitWith("newemail", "")
		return c.Send(err.Error() + "，请重新发送：")
	}
	if _, err := m.panel.GetClient(email); err == nil {
		c.AwaitWith("newemail", "")
		return c.Send(fmt.Sprintf("<b>%s</b> 已经存在，换一个标识发送：", bot.Escape(email)))
	}
	inbounds, err := m.panel.ListInbounds()
	if err != nil {
		return fmt.Errorf("读取入站失败: %w", err)
	}
	d := newDraft{Email: email, QuotaGB: -1, Days: -1}
	for _, in := range inbounds {
		if in.Enable {
			d.Inbounds = append(d.Inbounds, in.Id)
		}
	}
	switch {
	case len(inbounds) == 0:
		return c.Send("面板上还没有入站，客户端必须挂在至少一个入站上。")
	case len(inbounds) == 1:
		d.Inbounds = []int{inbounds[0].Id}
		return m.wizardQuota(c, d, false)
	}
	return m.wizardInbounds(c, d, inbounds, false)
}

func (m *Module) wizard(c *bot.Context, action string, args []string) error {
	var value, token string
	switch len(args) {
	case 1:
		token = args[0]
	case 2:
		value, token = args[0], args[1]
	default:
		return nil
	}
	d, ok := loadDraft(c, token)
	if !ok {
		return c.Replace("这一步已过期，请重新开始新增客户端。", bot.Keyboard(bot.InlineRow(bot.BackTo(c, "返回列表", "list"))))
	}
	n, _ := strconv.Atoi(value)
	switch action {
	case "ntog":
		if i := indexOf(d.Inbounds, n); i >= 0 {
			d.Inbounds = append(d.Inbounds[:i:i], d.Inbounds[i+1:]...)
		} else {
			d.Inbounds = append(d.Inbounds, n)
		}
		inbounds, err := m.panel.ListInbounds()
		if err != nil {
			return fmt.Errorf("读取入站失败: %w", err)
		}
		return m.wizardInbounds(c, d, inbounds, true)
	case "nnext":
		if len(d.Inbounds) == 0 {
			return c.Replace("至少要选一个入站。", bot.Keyboard(bot.InlineRow(bot.Button("← 重新选择", c.CallbackData("ntog", "0", d.stash(c))))))
		}
		return m.wizardQuota(c, d, true)
	case "nq":
		d.QuotaGB = n
		return m.wizardDays(c, d, true)
	case "nqask":
		c.AwaitWith("newquota", d.stash(c))
		return c.Replace("发送流量额度（GB），例如 <code>300</code>；发送 <code>0</code> 表示不限：")
	case "nd":
		d.Days = n
		return m.wizardConfirm(c, d, true)
	case "ndask":
		c.AwaitWith("newdays", d.stash(c))
		return c.Replace("发送有效天数（从今天起算），例如 <code>45</code>；发送 <code>0</code> 表示不限期：")
	case "ngo":
		return m.create(c, d)
	}
	return nil
}

func indexOf(list []int, v int) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

func (m *Module) wizardInbounds(c *bot.Context, d newDraft, inbounds []panel.InboundSlim, replace bool) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>新增客户端 · %s</b>\n\n挂到哪些入站？点按切换，默认选中所有已启用的入站。", bot.Escape(d.Email)))
	var rows [][]telego.InlineKeyboardButton
	for _, in := range inbounds {
		mark := "▫️"
		if indexOf(d.Inbounds, in.Id) >= 0 {
			mark = "✅"
		}
		label := fmt.Sprintf("%s %s · %s:%d", mark, inboundName(in), in.Protocol, in.Port)
		rows = append(rows, bot.InlineRow(bot.Button(label, c.CallbackData("ntog", strconv.Itoa(in.Id), d.stash(c)))))
	}
	rows = append(rows, bot.InlineRow(bot.Button("下一步 →", c.CallbackData("nnext", d.stash(c)))))
	rows = append(rows, bot.InlineRow(bot.BackTo(c, "取消", "list")))
	return send(c, replace, sb.String(), bot.Keyboard(rows...))
}

func inboundName(in panel.InboundSlim) string {
	if in.Remark != "" {
		return in.Remark
	}
	return in.Tag
}

func (m *Module) wizardQuota(c *bot.Context, d newDraft, replace bool) error {
	token := d.stash(c)
	row := []telego.InlineKeyboardButton{bot.Button("不限", c.CallbackData("nq", "0", token))}
	for _, gb := range quotaPresets {
		row = append(row, bot.Button(fmt.Sprintf("%d GB", gb), c.CallbackData("nq", strconv.Itoa(gb), token)))
	}
	return send(c, replace, fmt.Sprintf("<b>新增客户端 · %s</b>\n\n流量额度？", bot.Escape(d.Email)), bot.Keyboard(
		row,
		bot.InlineRow(bot.Button("自定义…", c.CallbackData("nqask", token))),
		bot.InlineRow(bot.BackTo(c, "取消", "list")),
	))
}

func (m *Module) wizardDays(c *bot.Context, d newDraft, replace bool) error {
	token := d.stash(c)
	row := []telego.InlineKeyboardButton{bot.Button("不限期", c.CallbackData("nd", "0", token))}
	for _, days := range expiryPresets {
		row = append(row, bot.Button(fmt.Sprintf("%d 天", days), c.CallbackData("nd", strconv.Itoa(days), token)))
	}
	return send(c, replace, fmt.Sprintf("<b>新增客户端 · %s</b>\n\n流量 %s。有效期？", bot.Escape(d.Email), quotaLabel(d.QuotaGB)), bot.Keyboard(
		row,
		bot.InlineRow(bot.Button("自定义…", c.CallbackData("ndask", token))),
		bot.InlineRow(bot.BackTo(c, "取消", "list")),
	))
}

func (m *Module) wizardConfirm(c *bot.Context, d newDraft, replace bool) error {
	inbounds, err := m.panel.ListInbounds()
	if err != nil {
		return fmt.Errorf("读取入站失败: %w", err)
	}
	var names []string
	for _, in := range inbounds {
		if indexOf(d.Inbounds, in.Id) >= 0 {
			names = append(names, inboundName(in))
		}
	}
	body := fmt.Sprintf("<b>确认新增客户端</b>\n\n标识    %s\n入站    %s\n流量    %s\n有效期  %s",
		bot.Escape(d.Email), bot.Escape(strings.Join(names, "、")), quotaLabel(d.QuotaGB), daysLabel(d.Days))
	return send(c, replace, body, bot.Keyboard(
		bot.InlineRow(bot.Button("✅ 创建", c.CallbackData("ngo", d.stash(c)))),
		bot.InlineRow(bot.BackTo(c, "取消", "list")),
	))
}

func quotaLabel(gb int) string {
	if gb == 0 {
		return "不限"
	}
	return fmt.Sprintf("%d GB", gb)
}

func daysLabel(days int) string {
	if days == 0 {
		return "不限期"
	}
	return fmt.Sprintf("%d 天（到 %s）", days, time.Now().AddDate(0, 0, days).Format("2006-01-02"))
}

func expiryFromToday(days int) int64 {
	if days == 0 {
		return 0
	}
	return time.Now().AddDate(0, 0, days).UnixMilli()
}

func (m *Module) create(c *bot.Context, d newDraft) error {
	client := map[string]any{
		"email":      d.Email,
		"enable":     true,
		"totalGB":    int64(d.QuotaGB) * gib,
		"expiryTime": expiryFromToday(d.Days),
		"limitIp":    0,
	}
	if err := m.panel.CreateClient(client, d.Inbounds); err != nil {
		return fmt.Errorf("创建客户端失败: %w", err)
	}
	return m.viewWithNote(c, d.Email, "✅ 已创建。订阅链接和二维码在下方。")
}

func (m *Module) expiryMenu(c *bot.Context, email string) error {
	d, err := m.panel.GetClient(email)
	if err != nil {
		return fmt.Errorf("读取客户端失败: %w", err)
	}
	ref := stashedRef(c, email)
	from := "今天"
	if exp := d.Int("expiryTime"); exp > 0 && time.UnixMilli(exp).After(time.Now()) {
		from = "原到期日"
	}
	var row []telego.InlineKeyboardButton
	for _, days := range expiryPresets {
		row = append(row, bot.Button(fmt.Sprintf("+%d 天", days), c.CallbackData("expset", strconv.Itoa(days), ref)))
	}
	return c.Replace(fmt.Sprintf("<b>%s 的有效期</b>\n\n当前：%s\n\n续期从%s算起。", bot.Escape(email), expiryText(d.Int("expiryTime")), from), bot.Keyboard(
		row,
		bot.InlineRow(
			bot.Button("不限期", c.CallbackData("expset", "0", ref)),
			bot.Button("从今天起…天", c.CallbackData("expask", ref)),
		),
		bot.InlineRow(bot.Back(c, "view", ref)),
	))
}

// setExpiry extends from the current expiry while it is still ahead, and from
// today once it has passed — renewing a lapsed client by 30 days should give
// it 30 usable days, not a date that is already behind.
func (m *Module) setExpiry(c *bot.Context, email string, days int) error {
	var next int64
	err := m.panel.UpdateClient(email, func(client map[string]any) {
		if days == 0 {
			next = 0
		} else {
			base := time.Now()
			if cur, _ := client["expiryTime"].(float64); cur > 0 && time.UnixMilli(int64(cur)).After(base) {
				base = time.UnixMilli(int64(cur))
			}
			next = base.AddDate(0, 0, days).UnixMilli()
		}
		client["expiryTime"] = next
	})
	if err != nil {
		return fmt.Errorf("修改有效期失败: %w", err)
	}
	return m.viewWithNote(c, email, "有效期已改为 "+expiryText(next)+"。"+m.stillDisabled(email))
}

// stillDisabled reminds that renewing or raising a quota does not re-enable a
// client the panel switched off when it ran out: the edit writes the flag back
// unchanged, and it may also have been switched off on purpose.
func (m *Module) stillDisabled(email string) string {
	if d, err := m.panel.GetClient(email); err == nil && !d.Bool("enable") {
		return "\n⚠️ 这个客户端目前是停用状态，修改额度或有效期不会自动启用；需要的话点「▶️ 启用」。"
	}
	return ""
}

func (m *Module) setExpiryFromToday(c *bot.Context, email string, days int) error {
	next := expiryFromToday(days)
	if err := m.panel.UpdateClient(email, func(client map[string]any) { client["expiryTime"] = next }); err != nil {
		return fmt.Errorf("修改有效期失败: %w", err)
	}
	return m.viewWithNote(c, email, "有效期已改为 "+expiryText(next)+"。"+m.stillDisabled(email))
}

func (m *Module) quotaMenu(c *bot.Context, email string) error {
	t, err := m.panel.ClientTraffic(email)
	if err != nil {
		return fmt.Errorf("读取客户端失败: %w", err)
	}
	ref := stashedRef(c, email)
	row := []telego.InlineKeyboardButton{bot.Button("不限", c.CallbackData("quotaset", "0", ref))}
	for _, gb := range quotaPresets {
		row = append(row, bot.Button(fmt.Sprintf("%d GB", gb), c.CallbackData("quotaset", strconv.Itoa(gb), ref)))
	}
	return c.Replace(fmt.Sprintf("<b>%s 的流量额度</b>\n\n当前：%s\n\n新额度是总量，已用流量不变。", bot.Escape(email), trafficText(t)), bot.Keyboard(
		row,
		bot.InlineRow(bot.Button("自定义…", c.CallbackData("quotaask", ref))),
		bot.InlineRow(bot.Back(c, "view", ref)),
	))
}

func (m *Module) setQuota(c *bot.Context, email string, gb int) error {
	if err := m.panel.UpdateClient(email, func(client map[string]any) { client["totalGB"] = int64(gb) * gib }); err != nil {
		return fmt.Errorf("修改流量额度失败: %w", err)
	}
	note := "流量额度已改为 " + quotaLabel(gb) + "。"
	if t, err := m.panel.ClientTraffic(email); err == nil && gb > 0 && t.Up+t.Down >= int64(gb)*gib {
		note += "\n⚠️ 已用流量已经超过新额度，面板会停用这个客户端。"
	} else {
		note += m.stillDisabled(email)
	}
	return m.viewWithNote(c, email, note)
}

func (m *Module) toggle(c *bot.Context, email string) error {
	d, err := m.panel.GetClient(email)
	if err != nil {
		return fmt.Errorf("读取客户端失败: %w", err)
	}
	enable := !d.Bool("enable")
	if err := m.panel.SetClientEnabled(email, enable); err != nil {
		return fmt.Errorf("修改状态失败: %w", err)
	}
	if enable {
		return m.viewWithNote(c, email, "已启用。")
	}
	return m.viewWithNote(c, email, "已停用，订阅和连接随即失效，可以随时重新启用。")
}

// delete keeps a copy before it removes anything: without one the deletion
// could not be undone, so a failed save cancels it.
func (m *Module) delete(c *bot.Context, email string) error {
	d, err := m.panel.GetClient(email)
	if err != nil {
		return fmt.Errorf("读取客户端失败: %w", err)
	}
	if err := m.store.SaveDeletedClient(state.DeletedClient{
		At: time.Now(), Email: email, Client: d.AsClient(), InboundIds: d.InboundIds,
	}); err != nil {
		return fmt.Errorf("保存撤销记录失败，未删除: %w", err)
	}
	if err := m.panel.DeleteClient(email); err != nil {
		_ = m.store.ForgetDeletedClient(email)
		return fmt.Errorf("删除失败: %w", err)
	}
	ref := stashedRef(c, email)
	return c.Replace(fmt.Sprintf("🗑 已删除 <b>%s</b>。", bot.Escape(email)), bot.Keyboard(
		bot.InlineRow(bot.Button("↩️ 撤销删除", c.CallbackData("undo", ref))),
		bot.InlineRow(c.ReturnButton("← 返回列表", "client:list")),
	))
}

func (m *Module) undoDelete(c *bot.Context, email string) error {
	d, ok := m.store.DeletedClient(email)
	if !ok {
		return c.Replace("没有这个客户端的删除记录，可能已经撤销过了。", bot.Keyboard(bot.InlineRow(c.ReturnButton("← 返回列表", "client:list"))))
	}
	if _, err := m.panel.GetClient(email); err == nil {
		return c.Replace(fmt.Sprintf("<b>%s</b> 已经存在，无法撤销删除。", bot.Escape(email)), bot.Keyboard(bot.InlineRow(c.ReturnButton("← 返回列表", "client:list"))))
	}
	if err := m.panel.CreateClient(d.Client, d.InboundIds); err != nil {
		return fmt.Errorf("撤销删除失败: %w", err)
	}
	if err := m.store.ForgetDeletedClient(email); err != nil {
		return err
	}
	return m.viewWithNote(c, email, "↩️ 已恢复，原来的订阅链接和分享链接重新可用。已用流量从零开始。")
}

func send(c *bot.Context, replace bool, text string, kb *telego.InlineKeyboardMarkup) error {
	if replace {
		return c.Replace(text, kb)
	}
	return c.Send(text, kb)
}
