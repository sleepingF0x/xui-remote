package integration_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	"github.com/sleepingF0x/xui-remote/internal/bot"
	"github.com/sleepingF0x/xui-remote/internal/modules/client"
	"github.com/sleepingF0x/xui-remote/internal/modules/configtxn"
	"github.com/sleepingF0x/xui-remote/internal/modules/inbound"
	"github.com/sleepingF0x/xui-remote/internal/modules/outbound"
	"github.com/sleepingF0x/xui-remote/internal/modules/routetest"
	"github.com/sleepingF0x/xui-remote/internal/modules/routing"
	"github.com/sleepingF0x/xui-remote/internal/modules/status"
	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/state"
	"github.com/sleepingF0x/xui-remote/internal/txn"
	"github.com/sleepingF0x/xui-remote/internal/verify"
)

type screen struct {
	ChatID    int64  `json:"chat_id"`
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
	Markup    struct {
		Rows [][]telego.InlineKeyboardButton `json:"inline_keyboard"`
	} `json:"reply_markup"`
}

type harness struct {
	t       *testing.T
	bot     *bot.Bot
	manager *txn.Manager
	store   *state.Store
	mu      sync.Mutex
	screens []screen
	live    json.RawMessage
	writes  int
	hook    func(http.ResponseWriter, *http.Request) bool
}

const initial = `{"outbounds":[{"tag":"A","protocol":"freedom"},{"tag":"B","protocol":"freedom"},{"tag":"C","protocol":"freedom"}],"routing":{"rules":[{"type":"field","domain":["domain:a.test"],"outboundTag":"A"},{"type":"field","domain":["domain:b.test"],"outboundTag":"B"},{"type":"field","domain":["domain:c.test"],"outboundTag":"C"}],"balancers":[{"tag":"auto","selector":["A"]}]}}`

func newHarness(t *testing.T, template string, hook func(http.ResponseWriter, *http.Request) bool) *harness {
	t.Helper()
	h := &harness{t: t, live: json.RawMessage(template), hook: hook}
	ps := httptest.NewServer(http.HandlerFunc(h.panelRequest))
	t.Cleanup(ps.Close)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "answerCallbackQuery") {
			fmt.Fprint(w, `{"ok":true,"result":true}`)
			return
		}
		var s screen
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			t.Errorf("Telegram request: %v", err)
		}
		h.mu.Lock()
		if s.MessageID == 0 {
			s.MessageID = len(h.screens) + 1
		}
		h.screens = append(h.screens, s)
		h.mu.Unlock()
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":%d,"type":"private"}}}`, s.MessageID, s.ChatID)
	}))
	t.Cleanup(ts.Close)
	api, err := telego.NewBot("123456789:"+strings.Repeat("a", 35), telego.WithAPIServer(ts.URL), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.NewStore(t.TempDir(), 20)
	if err != nil {
		t.Fatal(err)
	}
	p := panel.New(panel.Options{BaseURL: ps.URL, Token: "test", Timeout: 5 * time.Second})
	h.store = st
	h.manager = txn.New(p, st)
	h.bot = bot.NewWithAPI(api, []int64{1, 2})
	h.bot.Register(routing.New(h.manager), outbound.New(p, h.manager), inbound.New(p), routetest.New(p, h.manager), configtxn.New(h.manager, verify.New(p)), client.New(p), status.New(p))
	return h
}

func reply(w http.ResponseWriter, obj any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": obj})
}
func (h *harness) panelRequest(w http.ResponseWriter, r *http.Request) {
	if h.hook != nil && h.hook(w, r) {
		return
	}
	switch r.URL.Path {
	case "/xray/":
		h.mu.Lock()
		raw := append(json.RawMessage(nil), h.live...)
		h.mu.Unlock()
		reply(w, map[string]any{"xraySetting": raw, "outboundTestUrl": "https://example.test/probe"})
	case "/xray/update":
		_ = r.ParseForm()
		h.mu.Lock()
		h.live = json.RawMessage(r.Form.Get("xraySetting"))
		h.writes++
		h.mu.Unlock()
		reply(w, nil)
	case "/xray/routeTest":
		reply(w, map[string]any{"matched": true, "outboundTag": "C"})
	case "/server/status":
		reply(w, map[string]any{"cpu": 12.5, "xray": map[string]any{"state": "running", "version": "test"}})
	case "/xray/outbound-subs":
		reply(w, []any{})
	default:
		h.t.Errorf("unexpected panel request %s", r.URL.Path)
		http.Error(w, "unexpected request", 500)
	}
}
func (h *harness) send(chat int64, text string) {
	h.bot.HandleUpdate(context.Background(), telego.Update{Message: &telego.Message{MessageID: 10000, Chat: telego.Chat{ID: chat, Type: "private"}, Text: text}})
}
func (h *harness) last(chat int64) screen {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.screens) - 1; i >= 0; i-- {
		if h.screens[i].ChatID == chat {
			return h.screens[i]
		}
	}
	h.t.Fatal("no Telegram response")
	return screen{}
}
func button(s screen, label string) string {
	for _, row := range s.Markup.Rows {
		for _, b := range row {
			if b.Text == label {
				return b.CallbackData
			}
		}
	}
	return ""
}
func (h *harness) press(chat int64, label string) {
	h.t.Helper()
	s := h.last(chat)
	data := button(s, label)
	if data == "" {
		h.t.Fatalf("button %q missing from %+v", label, s)
	}
	h.tap(chat, s.MessageID, data)
}
func (h *harness) tap(chat int64, id int, data string) {
	h.bot.HandleUpdate(context.Background(), telego.Update{CallbackQuery: &telego.CallbackQuery{ID: "callback", Data: data, Message: &telego.Message{MessageID: id, Chat: telego.Chat{ID: chat, Type: "private"}}}})
}
func (h *harness) contains(chat int64, want string) {
	h.t.Helper()
	if s := h.last(chat); !strings.Contains(s.Text, want) {
		h.t.Fatalf("screen %q does not contain %q", s.Text, want)
	}
}

func TestSlashCommandButtonsAndReplies(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.send(1, "/rules@mybot")
	h.press(1, "🔍 筛选")
	h.contains(1, "发送出站名")
	h.send(1, "b.test")
	h.contains(1, "筛选")
	h.send(1, "/test")
	h.send(1, "example.com")
	h.contains(1, "→ <b>C</b>")
	h.send(1, "/status")
	h.press(1, "刷新")
	h.contains(1, "12.5%")
}

func (h *harness) openRule(chat int64, index int) {
	h.t.Helper()
	h.send(chat, "🔀 路由")
	for {
		s := h.last(chat)
		for _, row := range s.Markup.Rows {
			for _, b := range row {
				if strings.HasPrefix(b.Text, fmt.Sprintf("%d. ", index)) {
					h.tap(chat, s.MessageID, b.CallbackData)
					return
				}
			}
		}
		h.press(chat, "下一页 →")
	}
}
func (h *harness) retarget(chat int64, index int, target string) {
	h.t.Helper()
	h.openRule(chat, index)
	h.press(chat, "改出站")
	h.press(chat, target)
}
func TestEditDuringApplyIsNeverSilentlyLost(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/xray/update" {
			return false
		}
		close(entered)
		<-release
		return false
	})
	h.retarget(1, 1, "B")
	h.contains(1, "已暂存")
	h.send(1, "⚙️ 配置")
	h.press(1, "✅ 提交并重启")
	confirm := h.last(1)
	done := make(chan struct{})
	go func() { defer close(done); h.tap(1, confirm.MessageID, button(confirm, "确认提交")) }()
	waitFor(t, entered)
	h.retarget(2, 2, "C")
	feedback := h.last(2).Text
	unblock()
	waitFor(t, done)
	h.send(2, "⚙️ 配置")
	if strings.Contains(feedback, "已暂存") {
		h.contains(2, "C")
	} else if !strings.Contains(feedback, "重试") && !strings.Contains(feedback, "刷新") {
		t.Fatalf("new edit needs explicit feedback: %s", feedback)
	}
}

func TestOldConfirmationDoesNotDeleteAnotherRule(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.openRule(1, 2)
	h.press(1, "删除")
	old := h.last(1)
	h.openRule(1, 1)
	h.press(1, "删除")
	h.press(1, "确认删除")
	h.tap(1, old.MessageID, button(old, "确认删除"))
	h.contains(1, "刷新")
	h.send(1, "🔀 路由")
	h.contains(1, "b.test")
	h.contains(1, "c.test")
}

func TestNewBalancerRuleUsesBalancerField(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.send(1, "🔀 路由")
	h.press(1, "+ 新增规则")
	h.press(1, "auto（均衡器）")
	h.press(1, "域名 / 分类")
	h.send(1, "new.test")
	h.press(1, "末尾")
	h.contains(1, "已暂存")
	st, err := h.manager.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var tpl map[string]any
	_ = json.Unmarshal(st.Draft, &tpl)
	rules := tpl["routing"].(map[string]any)["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["balancerTag"] != "auto" || last["outboundTag"] != nil {
		t.Fatalf("incorrect target: %v", last)
	}
}

func TestRuleListLeavesTextAvailableAndReturnsToFilter(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.send(1, "🔀 路由")
	h.send(1, "example.com")
	h.contains(1, "要做什么")
	h.send(1, "/rules")
	h.press(1, "🔍 筛选")
	h.send(1, "b.test")
	s := h.last(1)
	found := ""
	for _, row := range s.Markup.Rows {
		for _, b := range row {
			if strings.Contains(b.Text, "b.test") && strings.Contains(b.Text, "B") {
				found = b.CallbackData
			}
		}
	}
	if found == "" {
		t.Fatal("missing meaningful rule button")
	}
	h.tap(1, s.MessageID, found)
	h.contains(1, "匹配条件")
	h.press(1, "原始 JSON")
	h.contains(1, "outboundTag")
	h.press(1, "← 返回")
	h.press(1, "← 返回列表")
	h.contains(1, "筛选")
	h.press(1, "🔍 清除筛选")
	if strings.Contains(h.last(1).Text, "筛选 “") {
		t.Fatal("filter wasn't cleared")
	}
	h.contains(1, "a.test")
}

func TestLongLogIsCompleteUTF8AndValidHTML(t *testing.T) {
	log := strings.Repeat("中文🙂<&>\"", 2500) + "\nend"
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/xray/getXrayResult" {
			reply(w, log)
			return true
		}
		return false
	})
	h.send(1, "/status")
	start := len(h.screens)
	h.press(1, "错误输出")
	var got strings.Builder
	for i, s := range h.screens[start:] {
		if !utf8.ValidString(s.Text) || len(s.Text) > 4096 {
			t.Fatalf("invalid or oversized chunk %d", i)
		}
		dec := xml.NewDecoder(strings.NewReader("<root>" + s.Text + "</root>"))
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("invalid HTML chunk %d: %v", i, err)
			}
			if chars, ok := tok.(xml.CharData); ok {
				got.Write(chars)
			}
		}
		if i < len(h.screens[start:])-1 && len(s.Markup.Rows) > 0 {
			t.Fatal("keyboard on intermediate chunk")
		}
	}
	if got.String() != "Xray 错误输出\n"+log {
		t.Fatal("log changed across chunks")
	}
	h.press(1, "← 返回")
	h.contains(1, "运行状态")
}

func (h *harness) apply(chat int64) {
	h.t.Helper()
	h.send(chat, "⚙️ 配置")
	h.press(chat, "✅ 提交并重启")
	h.press(chat, "确认提交")
}
func TestFinalPreviewAndVerificationIgnoreIntermediateTarget(t *testing.T) {
	calls := 0
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/xray/routeTest" {
			calls++
			reply(w, map[string]any{"matched": true, "outboundTag": "C"})
			return true
		}
		return false
	})
	h.retarget(1, 1, "B")
	h.retarget(1, 1, "C")
	h.send(1, "⚙️ 配置")
	h.contains(1, "最终变化")
	if strings.Contains(h.last(1).Text, "→ B") {
		t.Fatal("intermediate target in preview")
	}
	h.press(1, "✅ 提交并重启")
	h.press(1, "确认提交")
	h.contains(1, "1/1")
	if calls != 1 {
		t.Fatalf("verification ran %d times", calls)
	}
}

func TestSubscriptionURLIsEnteredOnceAndExplicitlyConfirmed(t *testing.T) {
	rawURL := "https://subscription.test/list?token=" + strings.Repeat("secret", 80)
	parsed, created := 0, 0
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/xray/outbound-subs/parse":
			_ = r.ParseForm()
			if r.Form.Get("url") != rawURL {
				t.Error("preview URL changed")
			}
			parsed++
			reply(w, []any{map[string]any{"tag": "node"}})
			return true
		case "/xray/outbound-subs":
			if r.Method == "POST" {
				_ = r.ParseForm()
				if r.Form.Get("url") != rawURL {
					t.Error("created URL changed")
				}
				created++
				reply(w, map[string]any{"id": 7, "remark": "example"})
				return true
			}
		case "/xray/outbound-subs/7/refresh":
			reply(w, []any{})
			return true
		}
		return false
	})
	h.send(1, rawURL)
	for _, row := range h.last(1).Markup.Rows {
		for _, b := range row {
			if len(b.CallbackData) > 64 {
				t.Fatal("callback too long")
			}
		}
	}
	h.press(1, "添加为出站订阅")
	h.contains(1, "解析到")
	if parsed != 1 || created != 0 {
		t.Fatalf("preview=%d create=%d", parsed, created)
	}
	h.press(1, "确认添加 (1 个节点)")
	h.contains(1, "已添加")
	if created != 1 {
		t.Fatalf("created %d", created)
	}
}

func TestClientReturnsToFilteredPageAndClearsSearch(t *testing.T) {
	lastSearch, lastPage := "", ""
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/clients/list/paged" {
			lastSearch, lastPage = r.URL.Query().Get("search"), r.URL.Query().Get("page")
			reply(w, map[string]any{"total": 16, "filtered": 16, "items": []any{map[string]any{"email": "alice", "enable": true}}})
			return true
		}
		if r.URL.Path == "/clients/traffic/alice" {
			reply(w, map[string]any{"email": "alice", "enable": true})
			return true
		}
		return false
	})
	h.send(1, "/client team")
	h.press(1, "下一页 →")
	h.press(1, "alice")
	h.press(1, "← 返回列表")
	if lastSearch != "team" || lastPage != "2" {
		t.Fatalf("lost list context: %s page %s", lastSearch, lastPage)
	}
	h.press(1, "🔍 清除搜索")
	if lastSearch != "" || lastPage != "1" {
		t.Fatal("search not directly cleared")
	}
}

func TestNetZeroNeverWritesOrRestarts(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.retarget(1, 1, "B")
	h.retarget(1, 1, "A")
	h.send(1, "⚙️ 配置")
	h.contains(1, "没有实际变化")
	if button(h.last(1), "✅ 提交并重启") != "" {
		t.Fatal("net-zero draft offers apply")
	}
	h.send(1, "/apply")
	h.contains(1, "无需提交")
	if _, err := h.manager.Apply(false); err != txn.ErrNoChanges {
		t.Fatalf("manager accepted net-zero apply: %v", err)
	}
	if h.writes != 0 {
		t.Fatal("net-zero draft restarted Xray")
	}
}
func TestStaleEditsAndConfirmationsHaveNoEffect(t *testing.T) {
	for _, action := range []string{"移动", "改出站", "匹配值", "丢弃", "提交", "跨聊天", "未知引用"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t, initial, nil)
			h.retarget(1, 1, "B")
			var old screen
			var data string
			switch action {
			case "移动":
				h.openRule(1, 2)
				old = h.last(1)
				data = button(old, "↑ 上移")
			case "改出站":
				h.openRule(1, 2)
				h.press(1, "改出站")
				old = h.last(1)
				data = button(old, "C")
			case "匹配值":
				h.openRule(1, 2)
				h.press(1, "改匹配值")
				h.press(1, "+ 添加值")
			case "丢弃":
				h.send(1, "⚙️ 配置")
				h.press(1, "丢弃")
				old = h.last(1)
				data = button(old, "确认丢弃")
			case "提交":
				h.send(1, "⚙️ 配置")
				h.press(1, "✅ 提交并重启")
				old = h.last(1)
				data = button(old, "确认提交")
			case "跨聊天":
				h.openRule(2, 2)
				old = h.last(2)
				data = button(old, "↑ 上移")
			case "未知引用":
				data = "route:ref:missing"
			}
			h.retarget(2, 3, "A")
			before, _ := h.manager.Pending()
			if action == "匹配值" {
				h.send(1, "added.test")
			} else {
				h.tap(1, old.MessageID, data)
			}
			h.contains(1, "刷新")
			after, _ := h.manager.Pending()
			if string(before.Draft) != string(after.Draft) || h.writes != 0 {
				t.Fatal("stale action mutated state")
			}
		})
	}
}
func TestVerificationTracksFinalPositionsAndSkipsDeletedRules(t *testing.T) {
	var probes []string
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/xray/routeTest" {
			_ = r.ParseForm()
			probes = append(probes, r.Form.Get("domain"))
			reply(w, map[string]any{"matched": true, "outboundTag": "B"})
			return true
		}
		return false
	})
	h.retarget(1, 1, "C")
	h.openRule(1, 1)
	h.press(1, "删除")
	h.press(1, "确认删除")
	h.openRule(1, 2)
	h.press(1, "↑ 上移")
	h.apply(1)
	for _, s := range probes {
		if s == "a.test" {
			t.Fatal("deleted rule was tested")
		}
	}
	report := h.last(1)
	var edit string
	for _, row := range report.Markup.Rows {
		for _, b := range row {
			if strings.Contains(b.Text, "调整第") {
				edit = b.CallbackData
				if b.Text != "调整第 1 条的位置" {
					t.Fatalf("wrong final index: %s", b.Text)
				}
			}
		}
	}
	if edit == "" {
		t.Fatal("missing adjustment for failed moved rule")
	}
	h.retarget(2, 2, "A")
	before, _ := h.manager.Pending()
	h.tap(1, report.MessageID, edit)
	h.contains(1, "刷新")
	after, _ := h.manager.Pending()
	if string(before.Draft) != string(after.Draft) {
		t.Fatal("old report changed new draft")
	}
}
func TestFinalBalancerVerificationAndUnprobeableConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		groups      []string
		want        string
	}{
		{"balancer", "", []string{"auto"}, "1/1"},
		{"missing path", "", nil, "1 项跳过"},
		{"source constraint", `,"source":["192.0.2.1"]`, []string{"auto"}, "1 项跳过"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tpl := strings.Replace(initial, `"domain:a.test"],"outboundTag"`, `"domain:a.test"]`+tc.extra+`,"outboundTag"`, 1)
			h := newHarness(t, tpl, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/xray/routeTest" {
					reply(w, map[string]any{"matched": true, "outboundTag": "node-1", "groupTags": tc.groups})
					return true
				}
				return false
			})
			h.retarget(1, 1, "auto（均衡器）")
			h.apply(1)
			h.contains(1, tc.want)
		})
	}
}

func TestPanelConflictAndRollbackPreservePendingDraft(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.retarget(1, 1, "B")
	h.mu.Lock()
	h.live = json.RawMessage(strings.Replace(initial, "domain:c.test", "domain:external.test", 1))
	h.mu.Unlock()
	h.send(1, "/apply")
	h.press(1, "确认提交")
	h.contains(1, "面板侧配置已变更")
	if h.writes != 0 {
		t.Fatal("external changes overwritten without confirmation")
	}
	h.press(1, "仍然覆盖提交")
	if h.writes != 1 {
		t.Fatal("explicit override did not apply")
	}
	h.send(1, "⚙️ 配置")
	h.press(1, "配置快照")
	snapshots := h.last(1)
	var rb string
	for _, row := range snapshots.Markup.Rows {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "回滚到 ") {
				rb = b.CallbackData
				break
			}
		}
	}
	h.tap(1, snapshots.MessageID, rb)
	confirm := h.last(1)
	h.retarget(2, 2, "C")
	h.tap(1, confirm.MessageID, button(confirm, "确认回滚"))
	h.contains(1, "刷新")
	h.send(1, "⚙️ 配置")
	h.press(1, "配置快照")
	snapshots = h.last(1)
	rb = ""
	for _, row := range snapshots.Markup.Rows {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "回滚到 ") {
				rb = b.CallbackData
				break
			}
		}
	}
	h.tap(1, snapshots.MessageID, rb)
	h.press(1, "确认回滚")
	h.contains(1, "先提交或丢弃")
	if h.writes != 1 {
		t.Fatal("rollback overwrote a pending draft")
	}
	h.send(1, "⚙️ 配置")
	h.press(1, "丢弃")
	h.press(1, "确认丢弃")
	h.send(1, "/rollback")
	snapshots = h.last(1)
	for _, row := range snapshots.Markup.Rows {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "回滚到 ") {
				rb = b.CallbackData
				break
			}
		}
	}
	h.tap(1, snapshots.MessageID, rb)
	h.press(1, "确认回滚")
	h.contains(1, "已回滚")
	if h.writes != 2 {
		t.Fatal("valid rollback did not execute")
	}
}
func TestRuleFilteredPageAndLongJSON(t *testing.T) {
	var tpl map[string]any
	_ = json.Unmarshal([]byte(initial), &tpl)
	var rules []any
	for i := 1; i <= 17; i++ {
		rules = append(rules, map[string]any{"type": "field", "domain": []any{fmt.Sprintf("domain:r%d.test", i)}, "outboundTag": "A", "user": []any{"alice"}, "inboundTag": []any{"in"}, "port": "443", "network": "tcp"})
	}
	tpl["routing"].(map[string]any)["rules"] = rules
	raw, _ := json.Marshal(tpl)
	h := newHarness(t, string(raw), nil)
	h.send(1, "/rules")
	h.press(1, "🔍 筛选")
	h.send(1, "r")
	h.press(1, "下一页 →")
	s := h.last(1)
	data := ""
	for _, row := range s.Markup.Rows {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "9. ") {
				data = b.CallbackData
			}
		}
	}
	if data == "" {
		t.Fatal("second page missing rule 9")
	}
	h.tap(1, s.MessageID, data)
	for _, want := range []string{"alice", "in", "443", "tcp", "线上配置"} {
		h.contains(1, want)
	}
	h.press(1, "原始 JSON")
	h.press(1, "← 返回")
	h.press(1, "← 返回列表")
	h.contains(1, "第 2 / 3 页")
	h.contains(1, "筛选")
	// Full JSON must survive several Telegram messages.
	rules[0].(map[string]any)["domain"] = []any{strings.Repeat("中🙂<&>", 2500)}
	raw, _ = json.Marshal(tpl)
	h = newHarness(t, string(raw), nil)
	h.openRule(1, 1)
	start := len(h.screens)
	h.press(1, "原始 JSON")
	var joined strings.Builder
	for _, s := range h.screens[start:] {
		dec := xml.NewDecoder(strings.NewReader("<root>" + s.Text + "</root>"))
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if chars, ok := tok.(xml.CharData); ok {
				joined.Write(chars)
			}
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(joined.String()), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rules[0]) {
		t.Fatal("raw JSON was truncated or changed")
	}
	h.press(1, "← 返回")
	h.contains(1, "匹配条件")
}

func TestTargetIdentityDoesNotDependOnDisplaySuffix(t *testing.T) {
	tpl := strings.ReplaceAll(initial, "\"A\"", "\"auto (balancer)\"")
	h := newHarness(t, tpl, nil)
	h.openRule(1, 1)
	h.press(1, "改出站")
	choice := h.last(1)
	for _, row := range choice.Markup.Rows {
		for _, button := range row {
			if strings.HasSuffix(button.Text, "auto（均衡器）") {
				h.tap(1, choice.MessageID, button.CallbackData)
			}
		}
	}
	h.contains(1, "已暂存")
	st, err := h.manager.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(st.Draft), "\"balancerTag\": \"auto\"") {
		t.Fatal("balancer target not saved")
	}
}

func TestRepeatedRuleEditsCollapseToOneFinalChange(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.retarget(1, 1, "B")
	h.openRule(1, 1)
	h.press(1, "改匹配值")
	h.press(1, "+ 添加值")
	h.send(1, "new.test")
	h.openRule(1, 1)
	h.press(1, "改匹配值")
	h.press(1, "✕ domain:a.test")
	h.send(1, "⚙️ 配置")
	h.contains(1, "最终变化（1 项）")
	for _, want := range []string{"outboundTag", "domain:a.test", "domain:new.test", "修改规则"} {
		h.contains(1, want)
	}
	if strings.Contains(h.last(1).Text, "删除规则") {
		t.Fatal("same rule became a deletion")
	}
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for controlled request")
	}
}
func TestMenuSwitchCannotResurrectOldPrompt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var armed atomic.Bool
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/xray/" && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return false
	})
	h.openRule(1, 1)
	h.press(1, "改匹配值")
	s := h.last(1)
	data := button(s, "+ 添加值")
	armed.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); h.tap(1, s.MessageID, data) }()
	waitFor(t, entered)
	h.send(1, "/status")
	h.contains(1, "运行状态")
	unblock()
	waitFor(t, done)
	h.send(1, "new.test")
	h.contains(1, "要做什么")
	if _, err := h.manager.Pending(); err == nil {
		t.Fatal("abandoned prompt staged a change")
	}
}
func TestConcurrentEditsKeepEveryAcknowledgedChange(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var armed atomic.Bool
	var reads atomic.Int32
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/xray/" && armed.Load() && reads.Add(1) <= 2 {
			entered <- struct{}{}
			<-release
		}
		return false
	})
	h.openRule(1, 1)
	h.press(1, "改出站")
	first := h.last(1)
	h.openRule(2, 2)
	h.press(2, "改出站")
	second := h.last(2)
	armed.Store(true)
	done := make(chan struct{}, 2)
	go func() { h.tap(1, first.MessageID, button(first, "B")); done <- struct{}{} }()
	go func() { h.tap(2, second.MessageID, button(second, "C")); done <- struct{}{} }()
	waitFor(t, entered)
	waitFor(t, entered)
	unblock()
	waitFor(t, done)
	waitFor(t, done)
	st, err := h.manager.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var tpl map[string]any
	_ = json.Unmarshal(st.Draft, &tpl)
	rules := tpl["routing"].(map[string]any)["rules"].([]any)
	for _, tc := range []struct {
		chat  int64
		index int
		want  string
	}{{1, 0, "B"}, {2, 1, "C"}} {
		feedback := h.last(tc.chat).Text
		if strings.Contains(feedback, "已暂存") {
			if rules[tc.index].(map[string]any)["outboundTag"] != tc.want {
				t.Fatal("acknowledged edit lost")
			}
		} else if !strings.Contains(feedback, "刷新") {
			t.Fatalf("missing rejection feedback: %s", feedback)
		}
	}
}

func TestEditingIdenticalNeighbourDoesNotInventMoves(t *testing.T) {
	tpl := strings.Replace(initial, "domain:b.test", "domain:a.test", 1)
	tpl = strings.Replace(tpl, "\"outboundTag\":\"B\"", "\"outboundTag\":\"A\"", 1)
	h := newHarness(t, tpl, nil)
	h.retarget(1, 1, "B")
	h.send(1, "⚙️ 配置")
	h.contains(1, "最终变化（1 项）")
	if strings.Contains(h.last(1).Text, "顺序调整") {
		t.Fatal("field edit invented a move")
	}
}

func TestOutboundAndInboundCommandsAndOldDelete(t *testing.T) {
	toggles := 0
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/xray/testOutbounds":
			reply(w, []any{map[string]any{"tag": "B", "success": true, "delay": 15}})
			return true
		case "/inbounds/list/slim":
			reply(w, []any{map[string]any{"id": 1, "remark": "入口", "enable": true}})
			return true
		case "/inbounds/setEnable/1":
			toggles++
			reply(w, nil)
			return true
		}
		return false
	})
	h.send(1, "/outbounds")
	h.press(1, "2")
	h.press(1, "删除")
	old := h.last(1)
	h.send(2, "📡 出站")
	h.press(2, "1")
	h.press(2, "删除")
	h.press(2, "确认删除")
	h.tap(1, old.MessageID, button(old, "确认删除"))
	h.contains(1, "刷新")
	h.send(1, "/outbounds")
	h.contains(1, "B")
	h.contains(1, "C")
	h.send(1, "/health")
	h.press(1, "重新测试")
	h.contains(1, "15 ms")
	h.send(1, "/inbounds")
	h.press(1, "停用 入口")
	h.press(1, "取消")
	h.contains(1, "入口")
	if toggles != 0 {
		t.Fatal("cancel toggled inbound")
	}
	h.press(1, "停用 入口")
	h.press(1, "确认停用")
	if toggles != 1 {
		t.Fatal("slash command confirmation didn't execute")
	}
}
func TestLegacyDraftWithoutMetadataStillEditsAndApplies(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.retarget(1, 1, "B")
	old, err := h.manager.Pending()
	if err != nil {
		t.Fatal(err)
	}
	old.Revision = ""
	old.RuleOrigins = nil
	old.Entries[0].Checks = []state.Check{{Kind: "domain", Value: "obsolete.test", Expect: "obsolete", Index: 99}}
	if err := h.store.SaveStaging(old); err != nil {
		t.Fatal(err)
	}
	h.retarget(1, 1, "C")
	h.apply(1)
	h.contains(1, "1/1")
	if h.writes != 1 {
		t.Fatal("legacy draft didn't apply")
	}
}
func TestNewRuleTargetsReachPanelAndNewThenDeletedIsNetZero(t *testing.T) {
	for _, choice := range []string{"A", "auto（均衡器）"} {
		t.Run(choice, func(t *testing.T) {
			h := newHarness(t, initial, nil)
			h.send(1, "/rules")
			h.press(1, "+ 新增规则")
			h.press(1, choice)
			h.press(1, "域名 / 分类")
			h.send(1, "new.test")
			h.press(1, "末尾")
			h.apply(1)
			var tpl map[string]any
			_ = json.Unmarshal(h.live, &tpl)
			rules := tpl["routing"].(map[string]any)["rules"].([]any)
			added := rules[3].(map[string]any)
			if choice == "A" {
				if added["outboundTag"] != "A" || added["balancerTag"] != nil {
					t.Fatal(added)
				}
			} else {
				if added["balancerTag"] != "auto" || added["outboundTag"] != nil {
					t.Fatal(added)
				}
			}
		})
	}
	h := newHarness(t, initial, nil)
	h.send(1, "/rules")
	h.press(1, "+ 新增规则")
	h.press(1, "A")
	h.press(1, "域名 / 分类")
	h.send(1, "new.test")
	h.press(1, "末尾")
	h.openRule(1, 4)
	h.press(1, "删除")
	h.press(1, "确认删除")
	h.send(1, "/diff")
	h.contains(1, "没有实际变化")
	if h.writes != 0 {
		t.Fatal("unapplied changes reached panel")
	}
}
