package bot

import "html"

// escape guards every value interpolated into an HTML-parse-mode message; a
// remark or error string containing "<" would otherwise break the whole send.
func escape(s string) string { return html.EscapeString(s) }

// Escape is the exported form for modules building their own message bodies.
func Escape(s string) string { return html.EscapeString(s) }

// Code wraps text in a tap-to-copy code span.
func Code(s string) string { return "<code>" + html.EscapeString(s) + "</code>" }
