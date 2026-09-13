package bot

import (
	"errors"
	"testing"
)

// A QR code is a photo message, and its back button edits text into it —
// which Telegram refuses. The screen still has to appear, so such errors fall
// back to a new message rather than surfacing as a 400.
func TestUneditableErrorsFallBackInsteadOfFailing(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want bool
	}{
		{"photo message", `telego: editMessageText: api: 400 "Bad Request: there is no text in the message to edit"`, true},
		{"not editable", `api: 400 "Bad Request: message can't be edited"`, true},
		{"aged out", `api: 400 "Bad Request: MESSAGE_ID_INVALID"`, true},
		{"deleted", `api: 400 "Bad Request: message to edit not found"`, true},
		// Transient failures must keep surfacing; swallowing them hides outages.
		{"network", "dial tcp: connection refused", false},
		{"rate limited", `api: 429 "Too Many Requests: retry after 5"`, false},
		{"unauthorized", `api: 401 "Unauthorized"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUneditable(errors.New(tt.err)); got != tt.want {
				t.Fatalf("isUneditable(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestUnmodifiedIsSilentButDistinctFromUneditable(t *testing.T) {
	err := errors.New(`api: 400 "Bad Request: message is not modified"`)
	if !isUnmodified(err) {
		t.Fatal("an unchanged edit must be treated as success")
	}
	if isUneditable(err) {
		t.Fatal("an unchanged edit must not trigger a duplicate message")
	}
}
