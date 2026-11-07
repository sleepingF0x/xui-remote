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
	panel         *panel.Client
	store         *state.Store
	mu            sync.Mutex
	epoch         atomic.Uint64
	settle        time.Duration
	confirmWindow time.Duration
}

func New(p *panel.Client, s *state.Store, opts ...Option) *Manager {
	m := &Manager{panel: p, store: s, settle: defaultSettle}
	for _, o := range opts {
		o(m)
	}
	return m
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
	// Health is the core's state once the write settled. Checked is false
	// when Xray was deliberately stopped, so nothing could be observed.
	Health Health
	// AlreadyLive means an earlier apply whose reply was lost had in fact
	// landed; this one only recorded it instead of writing again.
	AlreadyLive bool
	// ConfirmBy is when the change is rolled back unless kept; zero when no
	// confirmation window was armed.
	ConfirmBy time.Time
	// Merged names the regions the web panel changed after staging opened,
	// which were kept and went live together with the draft.
	Merged []string
}

// ConflictError is a draft and a web-panel edit that changed the same region
// of the template differently, so neither can be kept without losing the other.
type ConflictError struct {
	Regions []string
}

func (e *ConflictError) Error() string {
	return ErrConflict.Error() + "（" + strings.Join(e.Regions, "、") + "）"
}

func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// rebase moves staging onto live, the template the panel holds now, and saves
// it. The draft keeps only the operator's own change; see xraycfg.Rebase.
func (m *Manager) rebase(st *state.Staging, live json.RawMessage) ([]string, error) {
	draft, fromTheirs, conflicts, err := xraycfg.Rebase(st.Base, st.Draft, live)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		return nil, &ConflictError{Regions: conflicts}
	}
	if xraycfg.RebasedRules(fromTheirs) {
		// The draft's rules are now exactly the new base's.
		tpl, err := xraycfg.ParseTemplate(draft)
		if err != nil {
			return nil, err
		}
		rules, err := xraycfg.Rules(tpl)
		if err != nil {
			return nil, err
		}
		st.RuleOrigins = make([]int, len(rules))
		for i := range st.RuleOrigins {
			st.RuleOrigins[i] = i
		}
	}
	st.Base, st.Draft, st.Revision = live, draft, rand.Text()
	if err := m.store.SaveStaging(st); err != nil {
		return nil, err
	}
	return fromTheirs, nil
}

// Apply writes the draft to the panel after snapshotting what was live, so a
// bad change is one Rollback away. A change the core cannot run is written
// back out immediately and stays staged; see ApplyError.
func (m *Manager) Apply(force bool) (*ApplyResult, error) { return m.apply("", force, 0) }

// ApplyAt is Apply bound to the version the operator reviewed. chatID is
// where an expired confirmation window is reported.
func (m *Manager) ApplyAt(version string, force bool, chatID int64) (*ApplyResult, error) {
	if version == "" {
		return nil, ErrStale
	}
	return m.apply(version, force, chatID)
}

func (m *Manager) apply(version string, force bool, chatID int64) (*ApplyResult, error) {
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
	// The panel already holds the draft: an earlier apply landed but its reply
	// never arrived. Calling that a web-panel conflict would be wrong, and
	// writing it again would restart Xray for nothing.
	if sameJSON(live.Template, st.Draft) {
		return m.adoptLanded(st, chatID)
	}
	var merged []string
	if !force && !sameJSON(live.Template, st.Base) {
		// Whatever the web panel saved is already live; replaying the draft on
		// top keeps it, where overwriting would revert it and discarding would
		// throw away the operator's work.
		if merged, err = m.rebase(st, live.Template); err != nil {
			return nil, err
		}
		if sameJSON(live.Template, st.Draft) {
			return nil, ErrNoChanges
		}
	}
	// Whether the core is meant to run decides what a healthy result looks
	// like: the panel leaves a manually stopped core stopped.
	before, err := m.panel.Status()
	if err != nil {
		return nil, fmt.Errorf("读取 Xray 状态失败，未提交: %w", err)
	}
	wantRunning := before.Xray.State != stateStopped

	snap, err := m.store.SaveSnapshot(summarize(st.Entries), live.Template)
	if err != nil {
		return nil, err
	}
	if writeErr := m.panel.UpdateXraySetting(&panel.XraySetting{
		Template:        st.Draft,
		OutboundTestURL: live.OutboundTestURL,
	}); writeErr != nil {
		// The panel stores the template before reconciling the core, so an
		// error does not mean nothing changed. Read back what it now holds.
		after, readErr := m.panel.GetXraySetting()
		switch {
		case readErr != nil:
			return nil, fmt.Errorf("提交结果未知（%v），读取面板配置也失败（%v）。稍后打开暂存区确认", writeErr, readErr)
		case sameJSON(after.Template, live.Template):
			m.dropSnapshot(snap.ID)
			return nil, writeErr
		case !sameJSON(after.Template, st.Draft):
			return nil, fmt.Errorf("提交失败（%v），且面板配置已被其他改动替换", writeErr)
		}
		return nil, m.undo(snap, live, wantRunning, writeErr)
	}

	var health Health
	if wantRunning {
		health = m.ensureRunning()
		if health.Failed() {
			return nil, m.undo(snap, live, true, errors.New("新配置下 Xray 无法运行"))
		}
	}
	if err := m.store.ClearStaging(); err != nil {
		return nil, err
	}
	epoch := m.epoch.Add(1)
	res := &ApplyResult{Snapshot: snap, RuleOrigins: st.RuleOrigins, Applied: st.Entries, Base: st.Base, Draft: st.Draft,
		Version: fmt.Sprintf("live:%d:%s", epoch, digest(st.Draft)), Health: health, Merged: merged}
	if wantRunning {
		res.ConfirmBy = m.arm(snap.ID, st.Draft, chatID)
	} else {
		// Nothing is serving traffic, so the change cannot cut anyone off yet;
		// a probation window would only roll back a config nobody has used.
		m.disarm()
	}
	return res, nil
}

// adoptLanded finishes an apply whose write reached the panel earlier. The
// pre-change config is Base, which is what the panel held when staging opened.
func (m *Manager) adoptLanded(st *state.Staging, chatID int64) (*ApplyResult, error) {
	summary := summarize(st.Entries)
	// The attempt that landed normally left its snapshot behind already.
	var snap *state.Snapshot
	if snaps, err := m.store.ListSnapshots(); err == nil && len(snaps) > 0 &&
		snaps[0].Summary == summary && sameJSON(snaps[0].Template, st.Base) {
		snap = snaps[0]
	} else if snap, err = m.store.SaveSnapshot(summary, st.Base); err != nil {
		return nil, err
	}
	health := m.observe()
	if err := m.store.ClearStaging(); err != nil {
		return nil, err
	}
	epoch := m.epoch.Add(1)
	res := &ApplyResult{Snapshot: snap, RuleOrigins: st.RuleOrigins, Applied: st.Entries, Base: st.Base, Draft: st.Draft,
		Version: fmt.Sprintf("live:%d:%s", epoch, digest(st.Draft)), Health: health, AlreadyLive: true}
	if health.Running || health.Err != nil {
		res.ConfirmBy = m.arm(snap.ID, st.Draft, chatID)
	}
	return res, nil
}

// Rollback restores a snapshot, first snapshotting the current state so the
// rollback itself can be undone. A core that is down is started afterwards,
// since rolling back is how an operator recovers from a config that killed it.
func (m *Manager) Rollback(id string) (Health, error) { return m.rollback("", id) }
func (m *Manager) RollbackAt(version, id string) (Health, error) {
	if version == "" {
		return Health{}, ErrStale
	}
	return m.rollback(version, id)
}
func (m *Manager) rollback(version, id string) (Health, error) {
	if !m.mu.TryLock() {
		return Health{}, ErrBusy
	}
	defer m.mu.Unlock()
	if version != "" {
		if err := m.CheckVersion(version); err != nil {
			return Health{}, err
		}
	}
	if _, err := m.Pending(); err == nil {
		return Health{}, errors.New("请先提交或丢弃暂存改动，再回滚快照")
	} else if !errors.Is(err, ErrNothingStaged) {
		return Health{}, err
	}
	snap, err := m.store.GetSnapshot(id)
	if err != nil {
		return Health{}, err
	}
	live, err := m.panel.GetXraySetting()
	if err != nil {
		return Health{}, err
	}
	if _, err := m.store.SaveSnapshot("回滚前的状态", live.Template); err != nil {
		return Health{}, err
	}
	health, err := m.restore(&panel.XraySetting{Template: snap.Template, OutboundTestURL: live.OutboundTestURL}, m.coreWanted())
	if err != nil {
		return Health{}, err
	}
	m.epoch.Add(1)
	// The operator chose this config by hand; nothing is on probation now.
	m.disarm()
	return health, nil
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
