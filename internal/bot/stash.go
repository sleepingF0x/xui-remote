package bot

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Telegram caps callback_data at 64 bytes, which a domain plus parameters can
// exceed. Modules stash the real payload and put only a short token in the
// button; a token that has aged out answers as a stale button, not a panic.
const (
	stashCapacity = 512
	stashTTL      = 12 * time.Hour
)

type stashEntry struct {
	value string
	at    time.Time
}

type stash struct {
	mu     sync.Mutex
	items  map[string]stashEntry
	order  []string
	cursor int
}

func newStash() *stash {
	return &stash{items: make(map[string]stashEntry, stashCapacity)}
}

func (s *stash) put(value string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	token := hex.EncodeToString(buf[:])
	s.items[token] = stashEntry{value: value, at: time.Now()}
	s.order = append(s.order, token)
	for len(s.order) > stashCapacity {
		delete(s.items, s.order[0])
		s.order = s.order[1:]
	}
	return token
}

func (s *stash) get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[token]
	if !ok || time.Since(e.at) > stashTTL {
		return "", false
	}
	return e.value, true
}

// Stash stores a payload and returns a token short enough for callback data.
func (c *Context) Stash(value string) string { return c.b.stash.put(value) }

// Unstash resolves a token; ok is false for an expired or unknown button.
func (c *Context) Unstash(token string) (string, bool) { return c.b.stash.get(token) }
