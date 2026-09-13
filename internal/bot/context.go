package bot

import (
	"context"
	"fmt"
	"html"
	"strings"

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
	MessageID int
	Module    string
}

func (c *Context) Ctx() context.Context { return c.ctx }

// Send posts a new message, splitting anything over Telegram's size limit.
func (c *Context) Send(text string, keyboard ...*telego.InlineKeyboardMarkup) error {
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
	if c.MessageID == 0 || len(text) > telegramMessageLimit {
		return c.Send(text, keyboard...)
	}
	params := &telego.EditMessageTextParams{
		ChatID:    tu.ID(c.ChatID),
		MessageID: c.MessageID,
		Text:      text,
		ParseMode: telego.ModeHTML,
	}
	if len(keyboard) > 0 {
		params.ReplyMarkup = keyboard[0]
	}
	_, err := c.bot.EditMessageText(c.ctx, params)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

func (c *Context) Errorf(format string, args ...any) error {
	return c.Send("⚠️ " + html.EscapeString(fmt.Sprintf(format, args...)))
}

// Await routes the chat's next plain message back to this module under key.
func (c *Context) Await(key string) {
	c.b.setPrompt(c.ChatID, pendingPrompt{Module: c.Module, Key: key})
}

// AwaitWith is Await plus a payload handed back with the reply, so a multi-step
// wizard keeps its partial state without a second store.
func (c *Context) AwaitWith(key, payload string) {
	c.b.setPrompt(c.ChatID, pendingPrompt{Module: c.Module, Key: key, Payload: payload})
}

// CallbackData builds a namespaced callback for this module.
func (c *Context) CallbackData(action string, args ...string) string {
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

// splitMessage breaks text on line boundaries so a long list of links or rules
// arrives as several readable messages instead of being cut mid-entry.
func splitMessage(text string) []string {
	if len(text) <= telegramMessageLimit {
		return []string{text}
	}
	var out []string
	var buf strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if buf.Len()+len(line) > telegramMessageLimit && buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
		// A single line past the limit still has to be cut somewhere.
		for len(line) > telegramMessageLimit {
			out = append(out, line[:telegramMessageLimit])
			line = line[telegramMessageLimit:]
		}
		buf.WriteString(line)
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
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
