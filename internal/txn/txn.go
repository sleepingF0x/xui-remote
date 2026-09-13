// Package txn stages edits to the Xray config template and applies them as one
// transaction: /xray/update replaces the whole template and restarts the core,
// so batching keeps restarts to one per round of edits.
package txn

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/lichenhao/xui-remote/internal/panel"
	"github.com/lichenhao/xui-remote/internal/state"
)

type Manager struct {
	panel *panel.Client
	store *state.Store
}

func New(p *panel.Client, s *state.Store) *Manager {
	return &Manager{panel: p, store: s}
}

// ErrConflict means the live template changed since staging opened — applying
// the draft would discard whatever the web panel wrote in the meantime.
var ErrConflict = errors.New("面板侧配置已变更")

var ErrNothingStaged = errors.New("暂存区是空的")

// Edit applies mutate to the staged draft, opening staging from the live
// template on first use. Nothing reaches the panel until Apply.
func (m *Manager) Edit(module, summary string, mutate func(tpl map[string]any) error) error {
	st, err := m.store.LoadStaging()
	if errors.Is(err, state.ErrNoStaging) {
		live, liveErr := m.panel.GetXraySetting()
		if liveErr != nil {
			return liveErr
		}
		st = &state.Staging{Base: live.Template, Draft: live.Template}
	} else if err != nil {
		return err
	}

	var tpl map[string]any
	if err := json.Unmarshal(st.Draft, &tpl); err != nil {
		return fmt.Errorf("配置模板不是合法 JSON 对象: %w", err)
	}
	if err := mutate(tpl); err != nil {
		return err
	}
	draft, err := json.MarshalIndent(tpl, "", "  ")
	if err != nil {
		return err
	}
	st.Draft = draft
	st.Entries = append(st.Entries, state.StagedEntry{At: time.Now(), Module: module, Summary: summary})
	return m.store.SaveStaging(st)
}

// Inspect parses the live template without staging anything, for read-only views.
func (m *Manager) Inspect() (map[string]any, error) {
	st, err := m.store.LoadStaging()
	if err == nil {
		var tpl map[string]any
		if err := json.Unmarshal(st.Draft, &tpl); err != nil {
			return nil, err
		}
		return tpl, nil
	}
	if !errors.Is(err, state.ErrNoStaging) {
		return nil, err
	}
	live, err := m.panel.GetXraySetting()
	if err != nil {
		return nil, err
	}
	var tpl map[string]any
	if err := json.Unmarshal(live.Template, &tpl); err != nil {
		return nil, err
	}
	return tpl, nil
}

func (m *Manager) Pending() (*state.Staging, error) {
	st, err := m.store.LoadStaging()
	if errors.Is(err, state.ErrNoStaging) {
		return nil, ErrNothingStaged
	}
	return st, err
}

func (m *Manager) Discard() error { return m.store.ClearStaging() }

type ApplyResult struct {
	Snapshot *state.Snapshot
	Applied  []state.StagedEntry
}

// Apply writes the draft to the panel after snapshotting what was live, so a
// bad change is one Rollback away.
func (m *Manager) Apply(force bool) (*ApplyResult, error) {
	st, err := m.Pending()
	if err != nil {
		return nil, err
	}
	live, err := m.panel.GetXraySetting()
	if err != nil {
		return nil, err
	}
	if !force && !sameJSON(live.Template, st.Base) {
		return nil, ErrConflict
	}

	summary := summarize(st.Entries)
	snap, err := m.store.SaveSnapshot(summary, live.Template)
	if err != nil {
		return nil, err
	}
	if err := m.panel.UpdateXraySetting(&panel.XraySetting{
		Template:        st.Draft,
		OutboundTestURL: live.OutboundTestURL,
	}); err != nil {
		return nil, err
	}
	if err := m.store.ClearStaging(); err != nil {
		return nil, err
	}
	return &ApplyResult{Snapshot: snap, Applied: st.Entries}, nil
}

// Rollback restores a snapshot, first snapshotting the current state so the
// rollback itself can be undone.
func (m *Manager) Rollback(id string) error {
	snap, err := m.store.GetSnapshot(id)
	if err != nil {
		return err
	}
	live, err := m.panel.GetXraySetting()
	if err != nil {
		return err
	}
	if _, err := m.store.SaveSnapshot("回滚前的状态", live.Template); err != nil {
		return err
	}
	return m.panel.UpdateXraySetting(&panel.XraySetting{
		Template:        snap.Template,
		OutboundTestURL: live.OutboundTestURL,
	})
}

func (m *Manager) Snapshots() ([]*state.Snapshot, error) { return m.store.ListSnapshots() }

// sameJSON compares by value so a re-serialized template with reordered keys
// is not mistaken for a concurrent edit.
func sameJSON(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func summarize(entries []state.StagedEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, e.Summary)
	}
	return strings.Join(parts, "；")
}
