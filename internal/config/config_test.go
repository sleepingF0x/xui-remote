package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, ids string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "panel:\n  base_url: http://127.0.0.1/panel/api/\n  api_token: t\ntelegram:\n  token: t\n  allowed_chat_ids: " + ids + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Only a user id authorizes anyone; a list of groups alone loads fine and then
// silently answers nobody, which is worth saying at start-up.
func TestGroupOnlyAllowlistIsFlagged(t *testing.T) {
	if w := load(t, "[-100123]").Warnings(); len(w) != 1 || !strings.Contains(w[0], "用户 ID") {
		t.Fatalf("group-only list: %v", w)
	}
	for _, ids := range []string{"[42]", "[42, -100123]"} {
		if w := load(t, ids).Warnings(); len(w) != 0 {
			t.Fatalf("%s: unexpected warnings %v", ids, w)
		}
	}
}

func TestConfirmWindowDefaultsOnAndCanBeTurnedOff(t *testing.T) {
	if got := *load(t, "[42]").Apply.ConfirmMinutes; got != DefaultConfirmMinutes {
		t.Fatalf("default confirm window = %d", got)
	}
}
