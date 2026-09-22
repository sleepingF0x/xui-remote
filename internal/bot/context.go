package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// telegramMessageLimit is the hard cap on one message; longer output is split
// on line boundaries rather than truncated.
const telegramMessageLimit = 4096

type Context struct {
	ctx    context.Context
	bot    *telego.Bot
	b      *Bot
	ChatID int64
	// MessageID is the message carrying the inline keyboard, when this
	// Context came from a callback; zero for a plain message.
	MessageID      int
	Module         string
	Revision       string
	ReturnTo       string
	interaction    uint64
	obsoletePrompt bool
}

func (c *Context) Ctx() context.Context { return c.ctx }

// Send posts a new message, splitting anything over Telegram's size limit.
func (c *Context) Send(text string, keyboard ...*telego.InlineKeyboardMarkup) error {
	if c.obsoletePrompt {
		return nil
	}
	chunks := splitMessage(text)
	for i, chunk := range chunks {
		params := tu.Message(tu.ID(c.ChatID), chunk).WithParseMode(telego.ModeHTML)
		// Only the final chunk carries the keyboard, so the buttons sit at the
		// bottom of the output rather than in the middle of it.
		if i == len(chunks)-1 && len(keyboard) > 0 && keyboard[0] != nil {
			params = params.WithReplyMarkup(keyboard[0])
		}
		if _, err := c.bot.SendMessage(c.ctx, params); err != nil {
			return err
		}
	}
	return nil
}

// Replace edits the message the tapped button belongs to, falling back to a new
// message when there is nothing to edit or the text outgrew one message.
func (c *Context) Replace(text string, keyboard ...*telego.InlineKeyboardMarkup) error {
	if c.obsoletePrompt {
		return nil
	}
	if c.MessageID == 0 || len(text) > telegramMessageLimit {
		return c.Send(text, keyboard...)
	}
	params := &telego.EditMessageTextParams{
		ChatID:      tu.ID(c.ChatID),
		MessageID:   c.MessageID,
		Text:        text,
		ParseMode:   telego.ModeHTML,
		ReplyMarkup: Keyboard(),
	}
	if len(keyboard) > 0 {
		params.ReplyMarkup = keyboard[0]
	}
	_, err := c.bot.EditMessageText(c.ctx, params)
	switch {
	case err == nil:
		return nil
	case isUnmodified(err):
		return nil
	case isUneditable(err):
		// A photo or an aged-out message cannot take edited text; the screen
		// still has to appear, so post it instead of surfacing a 400.
		return c.Send(text, keyboard...)
	}
	return err
}

// isUnmodified reports Telegram rejecting an edit that would change nothing.
func isUnmodified(err error) bool {
	return strings.Contains(err.Error(), "message is not modified")
}

// isUneditable reports an edit Telegram will never accept for this message,
// as opposed to a transient failure worth surfacing.
func isUneditable(err error) bool {
	msg := err.Error()
	for _, s := range []string{
		"there is no text in the message to edit",
		"message can't be edited",
		"MESSAGE_ID_INVALID",
		"message to edit not found",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func (c *Context) Errorf(format string, args ...any) error {
	body := "⚠️ " + html.EscapeString(fmt.Sprintf(format, args...))
	if c.Module != "" {
		return c.Send(body, Keyboard(InlineRow(Button("刷新", CallbackData("core", "open", c.Module)))))
	}
	return c.Send(body)
}

// Await routes the chat's next plain message back to this module under key.
func (c *Context) Await(key string) {
	c.obsoletePrompt = !c.b.setPrompt(c.ChatID, c.interaction, pendingPrompt{Module: c.Module, Key: key, Revision: c.Revision, ReturnTo: c.ReturnTo})
}

// AwaitWith is Await plus a payload handed back with the reply, so a multi-step
// wizard keeps its partial state without a second store.
func (c *Context) AwaitWith(key, payload string) {
	c.obsoletePrompt = !c.b.setPrompt(c.ChatID, c.interaction, pendingPrompt{Module: c.Module, Key: key, Payload: payload, Revision: c.Revision, ReturnTo: c.ReturnTo})
}

// CallbackData builds a namespaced callback for this module.
func (c *Context) CallbackData(action string, args ...string) string {
	if c.Revision != "" || c.ReturnTo != "" {
		raw, _ := json.Marshal(callbackRef{Action: action, Args: args, Revision: c.Revision, ReturnTo: c.ReturnTo, ChatID: c.ChatID})
		return CallbackData(c.Module, "ref", c.Stash(string(raw)))
	}
	return CallbackData(c.Module, action, args...)
}

func CallbackData(module, action string, args ...string) string {
	parts := append([]string{module, action}, args...)
	return strings.Join(parts, ":")
}

func Button(label, data string) telego.InlineKeyboardButton {
	return tu.InlineKeyboardButton(label).WithCallbackData(data)
}

func Keyboard(rows ...[]telego.InlineKeyboardButton) *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(rows...)
}

// splitMessage keeps HTML tags balanced in every chunk. Escaped entities and
// UTF-8 runes are indivisible. The byte budget is conservative even for UTF-16
// Telegram clients, leaving room to close and reopen the few formatting tags.
func splitMessage(text string) []string {
	if len(text) <= telegramMessageLimit {
		return []string{text}
	}
	var chunks, stack []string
	var buf strings.Builder
	closeTags := func() string {
		var out strings.Builder
		for i := len(stack) - 1; i >= 0; i-- {
			out.WriteString("</" + stack[i] + ">")
		}
		return out.String()
	}
	flush := func() {
		chunks = append(chunks, buf.String()+closeTags())
		buf.Reset()
		for _, tag := range stack {
			buf.WriteString("<" + tag + ">")
		}
	}
	for len(text) > 0 {
		_, n := utf8.DecodeRuneInString(text)
		if text[0] == '<' {
			if end := strings.IndexByte(text, '>'); end >= 0 {
				n = end + 1
			}
		} else if text[0] == '&' {
			if end := strings.IndexByte(text, ';'); end >= 0 && end < 12 {
				n = end + 1
			}
		}
		token := text[:n]
		text = text[n:]
		if buf.Len()+len(token)+len(closeTags()) > telegramMessageLimit-64 {
			flush()
		}
		buf.WriteString(token)
		if strings.HasPrefix(token, "</") {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		} else if strings.HasPrefix(token, "<") {
			stack = append(stack, strings.TrimSuffix(strings.TrimPrefix(token, "<"), ">"))
		}
	}
	if buf.Len() > 0 {
		chunks = append(chunks, buf.String())
	}
	return chunks
}

// SendPhoto posts an in-memory image, used for QR codes.
func (c *Context) SendPhoto(name string, data []byte, caption string, keyboard ...*telego.InlineKeyboardMarkup) error {
	params := tu.Photo(tu.ID(c.ChatID), tu.FileFromBytes(data, name))
	if caption != "" {
		params = params.WithCaption(caption).WithParseMode(telego.ModeHTML)
	}
	if len(keyboard) > 0 && keyboard[0] != nil {
		params = params.WithReplyMarkup(keyboard[0])
	}
	_, err := c.bot.SendPhoto(c.ctx, params)
	return err
}

// A reference binds an object index to its entire configuration version.
// Navigation and prompt state travel with it, never in a shared chat cursor.
type callbackRef struct {
	Action             string
	Args               []string
	Revision, ReturnTo string
	ChatID             int64
}

func (c *Context) Expired() error {
	return c.Replace("配置或按钮已过期，请刷新后重试。", Keyboard(InlineRow(
		Button("刷新", CallbackData("core", "open", c.Module)))))
}
func (c *Context) ReturnButton(label, fallback string) telego.InlineKeyboardButton {
	dest := c.ReturnTo
	if dest == "" {
		dest = fallback
	}
	return Button(label, dest)
}
