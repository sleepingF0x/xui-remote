package bot

import "github.com/mymmrac/telego"

// Module is one feature area. Registering is the only wiring: the bot derives
// its menu button, its commands and its callback namespace from this.
type Module interface {
	// Name is the callback namespace; every callback this module emits is
	// "<name>:<action>[:args]" and no other module may claim the prefix.
	Name() string
	// Title labels the module's button on the persistent keyboard.
	Title() string
	// Menu renders the module's entry screen.
	Menu(c *Context) error
	// Callback handles one tap, with the namespace already stripped.
	Callback(c *Context, action string, args []string) error
}

// Commander is implemented by modules that also expose slash commands.
type Commander interface {
	Commands() []Command
}

type Command struct {
	Name        string
	Description string
	Handler     func(c *Context, args string) error
}

// TextMatcher lets a module offer actions for free-form text the user sends.
// Bare text is never executed directly — the bot shows the union of every
// module's offers and the user picks, so no module owns the input channel.
type TextMatcher interface {
	MatchText(input string) []TextOffer
}

type TextOffer struct {
	Label string
	// Callback is the full "<module>:<action>:<args>" string to fire on tap.
	Callback string
}

// Prompter is implemented by modules that ask a follow-up question; the bot
// routes the next message from that chat back to the module that asked.
type Prompter interface {
	Prompt(c *Context, key string, text string) error
}

// PayloadPrompter receives the payload passed to AwaitWith alongside the reply.
type PayloadPrompter interface {
	PromptWith(c *Context, key, payload, text string) error
}

// AwaitReply registers that the next plain message from this chat belongs to
// the named module and prompt key.
type pendingPrompt struct {
	Module  string
	Key     string
	Payload string
}

// InlineRow is a small helper so modules do not each re-import telego's
// keyboard builders for the common one-button-per-row case.
func InlineRow(buttons ...telego.InlineKeyboardButton) []telego.InlineKeyboardButton {
	return buttons
}
