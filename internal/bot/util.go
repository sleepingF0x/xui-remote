package bot

import (
	"html"

	"github.com/mymmrac/telego"
)

// escape guards every value interpolated into an HTML-parse-mode message; a
// remark or error string containing "<" would otherwise break the whole send.
func escape(s string) string { return html.EscapeString(s) }

// Escape is the exported form for modules building their own message bodies.
func Escape(s string) string { return html.EscapeString(s) }

// Code wraps text in a tap-to-copy code span.
func Code(s string) string { return "<code>" + html.EscapeString(s) + "</code>" }

// Back builds the standard "go up one level" button. Every screen gets one so
// a tap always has an escape that is not the persistent keyboard — which jumps
// to a module's top instead of back to where the user came from.
func Back(c *Context, action string, args ...string) telego.InlineKeyboardButton {
	return Button("← 返回", c.CallbackData(action, args...))
}

// BackTo labels the destination when "返回" alone is ambiguous.
func BackTo(c *Context, label, action string, args ...string) telego.InlineKeyboardButton {
	return Button("← "+label, c.CallbackData(action, args...))
}

// Home returns to the main menu; module top-level screens use it.
func Home() telego.InlineKeyboardButton {
	return Button("← 主菜单", "core:menu")
}
