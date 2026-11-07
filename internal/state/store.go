// Package state persists the staging area and config snapshots between restarts.
package state

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Staging holds edits that have not reached the panel yet. Base is the live
// template as it looked when staging opened, so Apply can detect a concurrent
// edit made in the web panel instead of silently overwriting it.
type Staging struct {
	RuleOrigins []int           `json:"rule_origins,omitempty"`
	Revision    string          `json:"revision,omitempty"`
	Base        json.RawMessage `json:"base"`
	Draft       json.RawMessage `json:"draft"`
	Entries     []StagedEntry   `json:"entries"`
}

type StagedEntry struct {
	At      time.Time `json:"at"`
	Module  string    `json:"module"`
	Summary string    `json:"summary"`
	// Checks is retained for decoding older drafts. New verification plans
	// are derived from Base and Draft after apply.
	Checks []Check `json:"checks,omitempty"`
}

// Check asserts that traffic matching Value ends up on Expect. Value may be a
// literal destination or a geosite/geoip token that has to be resolved to one.
type Check struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Expect string `json:"expect"`
	// User is the identity the probe must run as. A rule narrowed by user is
	// unreachable without it, so omitting it reports a failure that is not one.
	User string `json:"user,omitempty"`
	// Index is where the rule was placed, so a failure can name the earlier
	// rule that is shadowing it.
	Index    int    `json:"index"`
	Balancer bool   `json:"balancer,omitempty"`
	Skip     string `json:"skip,omitempty"`
	Port     int    `json:"port,omitempty"`
	Network  string `json:"network,omitempty"`
	Inbound  string `json:"inbound,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type Snapshot struct {
	ID       string          `json:"id"`
	At       time.Time       `json:"at"`
	Summary  string          `json:"summary"`
	Template json.RawMessage `json:"template"`
}

type Store struct {
	dir          string
	maxSnapshots int
	mu           sync.Mutex
}

var ErrNoStaging = errors.New("no staged changes")

func NewStore(dir string, maxSnapshots int) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, maxSnapshots: maxSnapshots}, nil
}

// Confirmation is an applied change still on probation: unless the operator
// keeps it before Deadline, the bot restores Snapshot. It is persisted so a bot
// restart inside the window cannot quietly make a bad change permanent.
type Confirmation struct {
	SnapshotID string `json:"snapshot_id"`
	// Applied is the template that went live; an auto-rollback only fires
	// while the panel still holds exactly this, so it never undoes later work.
	Applied  json.RawMessage `json:"applied"`
	Deadline time.Time       `json:"deadline"`
	ChatID   int64           `json:"chat_id"`
}

var ErrNoConfirmation = errors.New("no change awaiting confirmation")

func (s *Store) stagingPath() string { return filepath.Join(s.dir, "staging.json") }
func (s *Store) confirmPath() string { return filepath.Join(s.dir, "confirm.json") }

func (s *Store) LoadConfirmation() (*Confirmation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.confirmPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoConfirmation
	}
	if err != nil {
		return nil, err
	}
	var c Confirmation
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) SaveConfirmation(c *Confirmation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(s.confirmPath(), c)
}

func (s *Store) ClearConfirmation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.confirmPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) LoadStaging() (*Staging, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.stagingPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoStaging
	}
	if err != nil {
		return nil, err
	}
	var st Staging
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) SaveStaging(st *Staging) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(s.stagingPath(), st)
}

func (s *Store) ClearStaging() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.stagingPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// SaveSnapshot stores the template that was live before an apply, then trims
// the oldest so the directory cannot grow without bound.
func (s *Store) SaveSnapshot(summary string, template json.RawMessage) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	snap := &Snapshot{
		ID:       now.Format("20060102-150405") + "-" + rand.Text()[:8],
		At:       now,
		Summary:  summary,
		Template: template,
	}
	if err := writeAtomic(filepath.Join(s.dir, "snapshots", snap.ID+".json"), snap); err != nil {
		return nil, err
	}
	return snap, s.trimLocked()
}

func (s *Store) ListSnapshots() ([]*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() ([]*Snapshot, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "snapshots"))
	if err != nil {
		return nil, err
	}
	var out []*Snapshot
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, "snapshots", e.Name()))
		if err != nil {
			continue
		}
		var snap Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			continue
		}
		out = append(out, &snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func (s *Store) GetSnapshot(id string) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.dir, "snapshots", id+".json"))
	if err != nil {
		return nil, fmt.Errorf("快照 %s 不存在", id)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (s *Store) DeleteSnapshot(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.dir, "snapshots", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) trimLocked() error {
	all, err := s.listLocked()
	if err != nil {
		return err
	}
	for i := s.maxSnapshots; i < len(all); i++ {
		_ = os.Remove(filepath.Join(s.dir, "snapshots", all[i].ID+".json"))
	}
	return nil
}

// writeAtomic avoids leaving a half-written staging file behind if the bot is
// killed mid-save; a truncated one would fail to parse on the next start.
func writeAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeletedClient is enough to recreate a deleted client with the same
// credentials and subscription id, so the configs and links already handed
// out keep working after an undo. Only its traffic counters are lost.
type DeletedClient struct {
	At         time.Time      `json:"at"`
	Email      string         `json:"email"`
	Client     map[string]any `json:"client"`
	InboundIds []int          `json:"inbound_ids"`
}

// maxDeletedClients bounds the undo history; older deletions age out.
const maxDeletedClients = 20

func (s *Store) deletedClientsPath() string { return filepath.Join(s.dir, "deleted_clients.json") }

func (s *Store) loadDeletedLocked() ([]DeletedClient, error) {
	raw, err := os.ReadFile(s.deletedClientsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []DeletedClient
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SaveDeletedClient records a client about to be deleted, replacing an older
// record of the same email.
func (s *Store) SaveDeletedClient(d DeletedClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadDeletedLocked()
	if err != nil {
		return err
	}
	kept := []DeletedClient{d}
	for _, old := range list {
		if old.Email != d.Email && len(kept) < maxDeletedClients {
			kept = append(kept, old)
		}
	}
	return writeAtomic(s.deletedClientsPath(), kept)
}

func (s *Store) DeletedClient(email string) (*DeletedClient, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadDeletedLocked()
	if err != nil {
		return nil, false
	}
	for _, d := range list {
		if d.Email == email {
			return &d, true
		}
	}
	return nil, false
}

func (s *Store) ForgetDeletedClient(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadDeletedLocked()
	if err != nil {
		return err
	}
	kept := list[:0]
	for _, d := range list {
		if d.Email != email {
			kept = append(kept, d)
		}
	}
	return writeAtomic(s.deletedClientsPath(), kept)
}
