package bot

import "testing"

// Tapping a keyboard button while a prompt is open used to be read as the
// answer, so "🔀 路由" came back as "not a number, send 4 – 15".
func TestClassifyMessagePutsNavigationAheadOfAPendingPrompt(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		isTitle   bool
		hasPrompt bool
		want      messageKind
	}{
		{"command with no prompt", "/rules", false, false, kindCommand},
		{"command during a prompt", "/rules", false, true, kindCommand},
		{"keyboard button with no prompt", "🔀 路由", true, false, kindMenu},
		{"keyboard button during a prompt", "🔀 路由", true, true, kindMenu},
		{"answer during a prompt", "7", false, true, kindPrompt},
		{"free text with no prompt", "predict.fun", false, false, kindOffer},
		{"command-looking answer still navigates", "/start", false, true, kindCommand},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyMessage(tt.text, tt.isTitle, tt.hasPrompt); got != tt.want {
				t.Fatalf("classifyMessage(%q, title=%v, prompt=%v) = %v, want %v",
					tt.text, tt.isTitle, tt.hasPrompt, got, tt.want)
			}
		})
	}
}
