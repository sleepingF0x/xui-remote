// Package txn stages edits to the Xray config template and applies them as one
// transaction: /xray/update replaces the whole template and restarts the core,
// so batching keeps restarts to one per round of edits.
package txn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/state"
	"github.com/sleepingF0x/xui-remote/internal/xraycfg"
)

type Manager struct {
	panel *panel.Client
	store *state.Store
	mu    sync.Mutex
	epoch atomic.Uint64
}

func New(p *panel.Client, s *state.Store) *Manager {
	return &Manager{panel: p, store: s}
}

// ErrConflict means the live template changed since staging opened — applying
// the draft would discard whatever the web panel wrote in the meantime.
var ErrConflict = errors.New("面板侧配置已变更")

var ErrNothingStaged = errors.New("暂存区是空的")
var ErrStale = errors.New("配置已变化，请刷新后重试")
var ErrBusy = errors.New("正在处理其他配置操作，请稍后刷新重试")
var ErrNoChanges = errors.New("没有实际变化，无需提交")

// View pairs a template with the version any later edit must still match.
type View struct {
	Template map[string]any
	Version  string
	Staging  *state.Staging
}

func (m *Manager) View() (*View, error) {
	st, err := m.store.LoadStaging()
	var raw json.RawMessage
	var version string
	if err == nil {
		raw = st.Draft
		version = "draft:" + st.Revision + ":" + digest(raw)
	} else if errors.Is(err, state.ErrNoStaging) {
		live, err := m.panel.GetXraySetting()
		if err != nil {
			return nil, err
		}
		raw = live.Template
		version = fmt.Sprintf("live:%d:%s", m.epoch.Load(), digest(raw))
	} else {
		return nil, err
	}
	var tpl map[string]any
	if err := json.Unmarshal(raw, &tpl); err != nil {
		return nil, err
	}
	return &View{Template: tpl, Version: version, Staging: st}, nil
}

func digest(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		raw, _ = json.Marshal(v)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (m *Manager) CheckVersion(version string) error {
	if version == "" {
		return ErrStale
	}
	view, err := m.View()
	if err != nil {
		return err
	}
	if view.Version != version {
		return ErrStale
	}
	return nil
}

func (m *Manager) EditAt(version, module, summary string, mutate func(map[string]any) error) error {
	if version == "" {
		return ErrStale
	}
	return m.edit(version, module, summary, nil, mutate)
}

// Edit applies mutate to the staged draft, opening staging from the live
// template on first use. Nothing reaches the panel until Apply.
func (m *Manager) Edit(module, summary string, mutate func(tpl map[string]any) error) error {
	return m.EditWithChecks(module, summary, nil, mutate)
}

// EditWithChecks is Edit plus assertions to verify once the change is applied.
func (m *Manager) EditWithChecks(module, summary string, checks []state.Check, mutate func(tpl map[string]any) error) error {
	return m.edit("", module, summary, checks, mutate)
}

func (m *Manager) edit(version, module, summary string, checks []state.Check, mutate func(tpl map[string]any) error) error {
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	st, err := m.store.LoadStaging()
	if errors.Is(err, state.ErrNoStaging) {
		live, liveErr := m.panel.GetXraySetting()
		if liveErr != nil {
			return liveErr
		}
		if version != "" && version != fmt.Sprintf("live:%d:%s", m.epoch.Load(), digest(live.Template)) {
			return ErrStale
		}
		st = &state.Staging{Base: live.Template, Draft: live.Template}
	} else if err != nil {
		return err
	} else if version != "" && version != "draft:"+st.Revision+":"+digest(st.Draft) {
		return ErrStale
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
	origins, err := xraycfg.TrackRuleOrigins(st.Base, st.Draft, draft, st.RuleOrigins)
	if err != nil {
		return err
	}
	st.RuleOrigins = origins
	st.Draft = draft
	st.Revision = rand.Text()
	st.Entries = append(st.Entries, state.StagedEntry{
		At: time.Now(), Module: module, Summary: summary, Checks: checks,
	})
	return m.store.SaveStaging(st)
}

// Inspect parses the live template without staging anything, for read-only views.
func (m *Manager) Inspect() (map[string]any, error) {
	view, err := m.View()
	if err != nil {
		return nil, err
	}
	return view.Template, nil
}

func (m *Manager) Pending() (*state.Staging, error) {
	st, err := m.store.LoadStaging()
	if errors.Is(err, state.ErrNoStaging) {
		return nil, ErrNothingStaged
	}
	return st, err
}

func (m *Manager) Discard() error { return m.discard("") }
func (m *Manager) DiscardAt(version string) error {
	if version == "" {
		return ErrStale
	}
	return m.discard(version)
}
func (m *Manager) discard(version string) error {
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	if version != "" {
		if err := m.CheckVersion(version); err != nil {
			return err
		}
	}
	if err := m.store.ClearStaging(); err != nil {
		return err
	}
	m.epoch.Add(1)
	return nil
}

// PendingCount is how many edits are staged, or zero when the draft and the
// live config are the same thing.
func (m *Manager) PendingCount() int {
	st, err := m.store.LoadStaging()
	if err != nil {
		return 0
	}
	changes, err := xraycfg.Changes(st.Base, st.Draft, st.RuleOrigins)
	if err != nil {
		return len(st.Entries)
	}
	return len(changes)
}

// BaseTemplate is the live config as it looked when staging opened, so a view
// showing the draft can mark which parts of it are not live yet.
func (m *Manager) BaseTemplate() (map[string]any, bool) {
	st, err := m.store.LoadStaging()
	if err != nil {
		return nil, false
	}
	var tpl map[string]any
	if err := json.Unmarshal(st.Base, &tpl); err != nil {
		return nil, false
	}
	return tpl, true
}

type ApplyResult struct {
	Snapshot    *state.Snapshot
	RuleOrigins []int
	Applied     []state.StagedEntry
	Base        json.RawMessage
	Draft       json.RawMessage
	Version     string
}

// Apply writes the draft to the panel after snapshotting what was live, so a
// bad change is one Rollback away.
func (m *Manager) Apply(force bool) (*ApplyResult, error) { return m.apply("", force) }
func (m *Manager) ApplyAt(version string, force bool) (*ApplyResult, error) {
	if version == "" {
		return nil, ErrStale
	}
	return m.apply(version, force)
}
func (m *Manager) apply(version string, force bool) (*ApplyResult, error) {
	if !m.mu.TryLock() {
		return nil, ErrBusy
	}
	defer m.mu.Unlock()
	if version != "" {
		if err := m.CheckVersion(version); err != nil {
			return nil, err
		}
	}
	st, err := m.Pending()
	if err != nil {
		return nil, err
	}
	if sameJSON(st.Base, st.Draft) {
		return nil, ErrNoChanges
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
	epoch := m.epoch.Add(1)
	return &ApplyResult{Snapshot: snap, RuleOrigins: st.RuleOrigins, Applied: st.Entries, Base: st.Base, Draft: st.Draft,
		Version: fmt.Sprintf("live:%d:%s", epoch, digest(st.Draft))}, nil
}

// Rollback restores a snapshot, first snapshotting the current state so the
// rollback itself can be undone.
func (m *Manager) Rollback(id string) error { return m.rollback("", id) }
func (m *Manager) RollbackAt(version, id string) error {
	if version == "" {
		return ErrStale
	}
	return m.rollback(version, id)
}
func (m *Manager) rollback(version, id string) error {
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	if version != "" {
		if err := m.CheckVersion(version); err != nil {
			return err
		}
	}
	if _, err := m.Pending(); err == nil {
		return errors.New("请先提交或丢弃暂存改动，再回滚快照")
	} else if !errors.Is(err, ErrNothingStaged) {
		return err
	}
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
	if err := m.panel.UpdateXraySetting(&panel.XraySetting{Template: snap.Template, OutboundTestURL: live.OutboundTestURL}); err != nil {
		return err
	}
	m.epoch.Add(1)
	return nil
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
