// Package bot is the Telegram front end: whitelist, module registry, and the
// dispatch that turns messages and taps into module calls.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

type Bot struct {
	api            *telego.Bot
	allowed        map[int64]bool
	modules        []Module
	byName         map[string]Module
	byTitle        map[string]Module
	commands       map[string]Command
	commandModules map[string]string
	promptsMu      sync.Mutex
	prompts        map[int64]pendingPrompt
	interactions   map[int64]uint64
	stash          *stash
	keyboardKB     *telego.ReplyKeyboardMarkup
}

func New(token string, allowedChatIDs []int64) (*Bot, error) {
	api, err := telego.NewBot(token, telego.WithDefaultLogger(false, true))
	if err != nil {
		return nil, err
	}
	return NewWithAPI(api, allowedChatIDs), nil
}

// NewWithAPI uses an existing Telegram client, including its transport settings.
func NewWithAPI(api *telego.Bot, allowedChatIDs []int64) *Bot {
	allowed := make(map[int64]bool, len(allowedChatIDs))
	for _, id := range allowedChatIDs {
		allowed[id] = true
	}
	return &Bot{
		api:            api,
		allowed:        allowed,
		byName:         map[string]Module{},
		byTitle:        map[string]Module{},
		commands:       map[string]Command{},
		commandModules: map[string]string{},
		prompts:        map[int64]pendingPrompt{},
		interactions:   map[int64]uint64{},
		stash:          newStash(),
	}
}

func (b *Bot) Register(modules ...Module) {
	for _, m := range modules {
		if _, dup := b.byName[m.Name()]; dup {
			panic("duplicate module namespace: " + m.Name())
		}
		b.modules = append(b.modules, m)
		b.byName[m.Name()] = m
		b.byTitle[m.Title()] = m
		if c, ok := m.(Commander); ok {
			for _, cmd := range c.Commands() {
				b.commands[cmd.Name] = cmd
				b.commandModules[cmd.Name] = m.Name()
			}
		}
	}
	b.buildKeyboard()
}

func (b *Bot) Start(ctx context.Context) error {
	b.buildKeyboard()
	if err := b.publishCommands(ctx); err != nil {
		log.Printf("注册命令列表失败（不影响使用）: %v", err)
	}

	updates, err := b.api.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{Timeout: 30})
	if err != nil {
		return err
	}
	h, err := th.NewBotHandler(b.api, updates)
	if err != nil {
		return err
	}

	b.StartRunners(ctx)

	h.HandleMessage(func(_ *th.Context, msg telego.Message) error {
		go b.HandleUpdate(ctx, telego.Update{Message: &msg})
		return nil
	})
	h.HandleCallbackQuery(func(_ *th.Context, q telego.CallbackQuery) error {
		go b.HandleUpdate(ctx, telego.Update{CallbackQuery: &q})
		return nil
	}, th.AnyCallbackQueryWithMessage())

	return h.Start()
}

// StartRunners launches every module's background work. Start calls it; tests
// that drive updates by hand call it themselves.
func (b *Bot) StartRunners(ctx context.Context) {
	for _, m := range b.modules {
		r, ok := m.(Runner)
		if !ok {
			continue
		}
		name := m.Name()
		go b.safely(func() { r.Run(ctx, func(chatID int64) *Context { return b.Open(ctx, chatID, name) }) })
	}
}

// Open builds a Context for posting to chatID outside any interaction. A chat
// that is no longer allowed gets nothing.
func (b *Bot) Open(ctx context.Context, chatID int64, module string) *Context {
	if !b.allowed[chatID] {
		return nil
	}
	return b.context(ctx, chatID, 0, module)
}

// HandleUpdate dispatches one Telegram update through the same authorization
// and module routing used by the polling loop.
func (b *Bot) HandleUpdate(ctx context.Context, update telego.Update) {
	b.safely(func() {
		if update.Message != nil {
			b.onMessage(ctx, *update.Message)
		} else if update.CallbackQuery != nil && update.CallbackQuery.Message != nil {
			b.onCallback(ctx, *update.CallbackQuery)
		}
	})
}

// safely keeps one bad update from taking the whole bot down; telego's
// dispatch has no recovery of its own.
func (b *Bot) safely(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("handler panic: %v", r)
		}
	}()
	fn()
}

func (b *Bot) buildKeyboard() {
	var rows [][]telego.KeyboardButton
	var row []telego.KeyboardButton
	for _, m := range b.modules {
		row = append(row, tu.KeyboardButton(m.Title()))
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	b.keyboardKB = tu.Keyboard(rows...).WithResizeKeyboard()
}

func (b *Bot) publishCommands(ctx context.Context) error {
	cmds := []telego.BotCommand{{Command: "start", Description: "显示菜单"}}
	for _, m := range b.modules {
		c, ok := m.(Commander)
		if !ok {
			continue
		}
		for _, cmd := range c.Commands() {
			cmds = append(cmds, telego.BotCommand{Command: cmd.Name, Description: cmd.Description})
		}
	}
	return b.api.SetMyCommands(ctx, &telego.SetMyCommandsParams{Commands: cmds})
}

func (b *Bot) context(ctx context.Context, chatID int64, messageID int, module string) *Context {
	return &Context{ctx: ctx, bot: b.api, b: b, ChatID: chatID, MessageID: messageID, Module: module, interaction: interactionNumber(ctx)}
}

func (b *Bot) onMessage(ctx context.Context, msg telego.Message) {
	chatID := msg.Chat.ID
	// Silent drop, not a refusal: an unlisted chat should learn nothing about
	// what this bot is or whether the id it guessed exists.
	if !b.allowed[chatID] {
		log.Printf("拒绝来自未授权 chat %d 的消息", chatID)
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	ctx = b.beginInteraction(ctx, chatID)
	menuModule, isTitle := b.byTitle[text]
	switch classifyMessage(text, isTitle, b.hasPrompt(chatID)) {
	case kindCommand:
		b.clearPrompt(chatID, interactionNumber(ctx))
		b.handleCommand(ctx, chatID, text)
		return
	case kindMenu:
		b.clearPrompt(chatID, interactionNumber(ctx))
		c := b.context(ctx, chatID, 0, menuModule.Name())
		if err := menuModule.Menu(c); err != nil {
			_ = c.Errorf("%v", err)
		}
		return
	case kindOffer:
		b.offerActions(ctx, chatID, text)
		return
	}

	if prompt, ok := b.takePrompt(chatID, interactionNumber(ctx)); ok {
		if m, found := b.byName[prompt.Module]; found {
			c := b.context(ctx, chatID, 0, m.Name())
			c.Revision, c.ReturnTo = prompt.Revision, prompt.ReturnTo
			if p, ok := m.(PayloadPrompter); ok {
				if err := p.PromptWith(c, prompt.Key, prompt.Payload, text); err != nil {
					_ = c.Errorf("%v", err)
				}
				return
			}
			if p, ok := m.(Prompter); ok {
				if err := p.Prompt(c, prompt.Key, text); err != nil {
					_ = c.Errorf("%v", err)
				}
				return
			}
		}
	}
	b.offerActions(ctx, chatID, text)
}

type messageKind int

const (
	kindCommand messageKind = iota
	kindMenu
	kindPrompt
	kindOffer
)

// classifyMessage decides what a plain message means. Navigation — a command or
// a persistent-keyboard button — outranks a pending question: those are always
// "take me elsewhere", and answering a prompt with one strands the user inside
// a step they were trying to leave.
func classifyMessage(text string, isKeyboardTitle, hasPrompt bool) messageKind {
	switch {
	case strings.HasPrefix(text, "/"):
		return kindCommand
	case isKeyboardTitle:
		return kindMenu
	case hasPrompt:
		return kindPrompt
	default:
		return kindOffer
	}
}

// offerActions is the bare-text dispatcher: it never executes anything, it
// collects what each module could do with the input and lets the user choose.
// That keeps plain text available for every feature added later.
func (b *Bot) offerActions(ctx context.Context, chatID int64, text string) {
	var rows [][]telego.InlineKeyboardButton
	for _, m := range b.modules {
		matcher, ok := m.(TextMatcher)
		if !ok {
			continue
		}
		for _, offer := range matcher.MatchText(text) {
			parts := strings.SplitN(offer.Callback, ":", 3)
			if len(parts) < 2 {
				continue
			}
			var args []string
			if len(parts) == 3 {
				args = []string{parts[2]}
			}
			raw, _ := json.Marshal(callbackRef{Action: parts[1], Args: args, ChatID: chatID})
			data := CallbackData(parts[0], "ref", b.stash.put(string(raw)))
			rows = append(rows, []telego.InlineKeyboardButton{Button(offer.Label, data)})
		}
	}
	c := b.context(ctx, chatID, 0, "")
	if len(rows) == 0 {
		_ = c.Send("没认出这是什么。用下方键盘选一个功能，或 /start 看菜单。")
		return
	}
	rows = append(rows, []telego.InlineKeyboardButton{Button("取消", "core:cancel")})
	_ = c.Send(fmt.Sprintf("收到 <code>%s</code>，要做什么？", escape(text)), Keyboard(rows...))
}

func (b *Bot) handleCommand(ctx context.Context, chatID int64, text string) {
	name, args, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	// Telegram appends @botname when a command is used in a group.
	name, _, _ = strings.Cut(name, "@")
	c := b.context(ctx, chatID, 0, "")

	if name == "start" || name == "help" {
		_, err := b.api.SendMessage(ctx, tu.Message(tu.ID(chatID), b.menuText()).
			WithParseMode(telego.ModeHTML).WithReplyMarkup(b.keyboardKB))
		if err != nil {
			log.Printf("发送菜单失败: %v", err)
		}
		return
	}
	cmd, ok := b.commands[name]
	if !ok {
		_ = c.Send("未知命令：/" + escape(name))
		return
	}
	c.Module = b.commandModules[name]
	if err := cmd.Handler(c, strings.TrimSpace(args)); err != nil {
		_ = c.Errorf("%v", err)
	}
}

func (b *Bot) onCallback(ctx context.Context, q telego.CallbackQuery) {
	chatID := q.Message.GetChat().ID
	if !b.allowed[chatID] {
		return
	}
	ctx = b.beginInteraction(ctx, chatID)
	// Answering clears the client-side spinner even when the work takes a while.
	_ = b.api.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	// Tapping any button abandons a typed question — otherwise cancelling out of
	// a step leaves the prompt armed to eat the next unrelated message.
	b.clearPrompt(chatID, interactionNumber(ctx))

	parts := strings.Split(q.Data, ":")
	if len(parts) < 2 {
		return
	}
	messageID := 0
	if msg, ok := q.Message.(*telego.Message); ok {
		messageID = msg.MessageID
	}
	if parts[0] == "core" {
		c := b.context(ctx, chatID, messageID, "")
		switch parts[1] {
		case "cancel":
			_ = c.Replace("已取消。")
		case "open":
			if len(parts) > 2 {
				if m, ok := b.byName[parts[2]]; ok {
					c.Module = m.Name()
					if err := m.Menu(c); err != nil {
						_ = c.Errorf("%v", err)
					}
				}
			}
		case "menu":
			_ = c.Replace(b.menuText())
		}
		return
	}
	m, ok := b.byName[parts[0]]
	if !ok {
		return
	}
	c := b.context(ctx, chatID, messageID, m.Name())
	action, args := parts[1], parts[2:]
	if action == "ref" {
		var ref callbackRef
		if len(args) != 1 {
			_ = c.Expired()
			return
		}
		raw, found := c.Unstash(args[0])
		if !found || json.Unmarshal([]byte(raw), &ref) != nil || ref.ChatID != chatID {
			_ = c.Expired()
			return
		}
		action, args = ref.Action, ref.Args
		c.Revision, c.ReturnTo = ref.Revision, ref.ReturnTo
	}
	// Existing modules use colon-separated argument fragments.
	args = strings.Split(strings.Join(args, ":"), ":")
	if len(args) == 1 && args[0] == "" {
		args = nil
	}
	if err := m.Callback(c, action, args); err != nil {
		_ = c.Errorf("%v", err)
	}
}

func (b *Bot) menuText() string {
	var sb strings.Builder
	sb.WriteString("<b>x-ui 远程配置</b>\n\n")
	for _, m := range b.modules {
		sb.WriteString("• " + escape(m.Title()) + "\n")
	}
	sb.WriteString("\n用下方键盘进入，或直接发送域名/IP/邮箱。")
	return sb.String()
}

func (b *Bot) setPrompt(chatID int64, generation uint64, p pendingPrompt) bool {
	b.promptsMu.Lock()
	defer b.promptsMu.Unlock()
	if b.interactions[chatID] != generation {
		return false
	}
	b.prompts[chatID] = p
	return true
}

func (b *Bot) takePrompt(chatID int64, generation uint64) (pendingPrompt, bool) {
	b.promptsMu.Lock()
	defer b.promptsMu.Unlock()
	if b.interactions[chatID] != generation {
		return pendingPrompt{}, false
	}
	p, ok := b.prompts[chatID]
	delete(b.prompts, chatID)
	return p, ok
}

func (b *Bot) hasPrompt(chatID int64) bool {
	b.promptsMu.Lock()
	defer b.promptsMu.Unlock()
	_, ok := b.prompts[chatID]
	return ok
}

func (b *Bot) clearPrompt(chatID int64, generation uint64) {
	b.promptsMu.Lock()
	defer b.promptsMu.Unlock()
	if b.interactions[chatID] == generation {
		delete(b.prompts, chatID)
	}
}

// A slow callback may finish after a newer menu/cancel action. Only the
// latest interaction may register or clear a prompt for this chat.
type interactionKey struct{}

func interactionNumber(ctx context.Context) uint64 {
	n, _ := ctx.Value(interactionKey{}).(uint64)
	return n
}
func (b *Bot) beginInteraction(ctx context.Context, chatID int64) context.Context {
	b.promptsMu.Lock()
	b.interactions[chatID]++
	n := b.interactions[chatID]
	b.promptsMu.Unlock()
	return context.WithValue(ctx, interactionKey{}, n)
}
