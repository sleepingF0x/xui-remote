package txn

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/state"
)

// The panel's ProcessState values. "stop" is a core someone stopped on
// purpose; "error" is one that exited on its own.
const (
	stateRunning = "running"
	stateStopped = "stop"
)

// defaultSettle is how long a freshly started core is given before its state
// is read. A config Xray rejects at start-up makes it exit within this window.
const defaultSettle = 1500 * time.Millisecond

type Option func(*Manager)

// WithConfirmWindow arms a probation window after every apply: the change is
// rolled back unless the operator keeps it in time. Zero disables it.
func WithConfirmWindow(d time.Duration) Option { return func(m *Manager) { m.confirmWindow = d } }

// WithSettle overrides how long to wait before reading the core's state.
func WithSettle(d time.Duration) Option { return func(m *Manager) { m.settle = d } }

// Health is what the core reported once a write settled.
type Health struct {
	// Checked is false when the core was deliberately stopped and so was
	// never expected to run the new config.
	Checked bool
	Running bool
	State   string
	// Detail is the core's error output, or why it still runs the old config.
	Detail string
	// Err means the state could not be read at all.
	Err error
}

// Failed is an observed failure, not an unknown: a status read that errored
// proves nothing about the core and must not trigger an undo by itself.
func (h Health) Failed() bool { return h.Checked && h.Err == nil && !h.Running }

// ApplyError is a draft that reached the panel but that the core could not
// run. The previous template has been written back and the draft stays staged,
// so the operator can fix it and apply again.
type ApplyError struct {
	Cause      error
	Detail     string
	Restored   bool
	RestoreErr error
	// Health is the core's state after the restore.
	Health Health
}

func (e *ApplyError) Error() string {
	if e.Restored {
		return fmt.Sprintf("提交未生效（%v），已恢复原配置，改动仍在暂存区", e.Cause)
	}
	return fmt.Sprintf("提交未生效（%v），恢复原配置也失败：%v", e.Cause, e.RestoreErr)
}

func (e *ApplyError) Unwrap() error { return e.Cause }

// observe reads the core's state twice, a settle period apart, so a core that
// starts and then exits on a bad config is not reported as running.
func (m *Manager) observe() Health {
	h := Health{Checked: true}
	for range 2 {
		time.Sleep(m.settle)
		s, err := m.panel.Status()
		if err != nil {
			h.Err = err
			return h
		}
		h.State, h.Detail = s.Xray.State, s.Xray.ErrorMsg
		h.Running = s.Xray.State == stateRunning
		if !h.Running {
			return h
		}
	}
	return h
}

// ensureRunning observes the core and starts it once if it is down. The panel
// only reconciles a core that is already up, so one that a previous config
// killed would otherwise stay down even after a good config is written.
func (m *Manager) ensureRunning() Health {
	h := m.observe()
	if !h.Failed() {
		return h
	}
	if err := m.panel.RestartXray(); err != nil {
		if h.Detail == "" {
			h.Detail = err.Error()
		}
		return h
	}
	return m.observe()
}

// coreWanted reports whether the core should be running after a write. An
// unreadable state counts as wanted: every caller is a recovery path, where
// leaving Xray down costs more than starting one someone had stopped.
func (m *Manager) coreWanted() bool {
	s, err := m.panel.Status()
	return err != nil || s.Xray.State != stateStopped
}

// restore writes prev back. Like an apply, a reconcile failure may still have
// stored the template, so success is judged by reading it back.
func (m *Manager) restore(prev *panel.XraySetting, wantRunning bool) (Health, error) {
	if err := m.panel.UpdateXraySetting(prev); err != nil {
		after, readErr := m.panel.GetXraySetting()
		if readErr != nil || !sameJSON(after.Template, prev.Template) {
			return Health{}, err
		}
	}
	if !wantRunning {
		return Health{}, nil
	}
	return m.ensureRunning(), nil
}

// undo puts back the template that was live before a failed apply and leaves
// staging untouched, so the draft can be corrected and retried.
func (m *Manager) undo(snap *state.Snapshot, prev *panel.XraySetting, wantRunning bool, cause error) error {
	e := &ApplyError{Cause: cause}
	if s, err := m.panel.Status(); err == nil {
		e.Detail = s.Xray.ErrorMsg
	}
	if e.Detail == "" {
		if out, err := m.panel.XrayResult(); err == nil {
			e.Detail = out
		}
	}
	e.Health, e.RestoreErr = m.restore(prev, wantRunning)
	e.Restored = e.RestoreErr == nil
	if e.Restored {
		// The snapshot is of a config that is live again; listing it as the
		// state before a commit would describe a commit that never happened.
		m.dropSnapshot(snap.ID)
	}
	m.epoch.Add(1)
	return e
}

func (m *Manager) dropSnapshot(id string) {
	if err := m.store.DeleteSnapshot(id); err != nil {
		log.Printf("删除快照 %s 失败: %v", id, err)
	}
}

// arm starts the probation window for a change that just went live. A newer
// apply replaces any older window: the operator was reachable to make it.
func (m *Manager) arm(snapshotID string, applied json.RawMessage, chatID int64) time.Time {
	if m.confirmWindow <= 0 {
		m.disarm()
		return time.Time{}
	}
	deadline := time.Now().Add(m.confirmWindow)
	if err := m.store.SaveConfirmation(&state.Confirmation{
		SnapshotID: snapshotID, Applied: applied, Deadline: deadline, ChatID: chatID,
	}); err != nil {
		log.Printf("保存确认窗口失败，本次改动不会自动回滚: %v", err)
		return time.Time{}
	}
	return deadline
}

func (m *Manager) disarm() {
	if err := m.store.ClearConfirmation(); err != nil {
		log.Printf("清除确认窗口失败: %v", err)
	}
}

var ErrNotAwaiting = errors.New("这次改动已经确认过，或已不在确认期内")

// Keep ends the probation window of the change applied with snapshotID. It
// waits for the lock rather than failing fast: losing this race to a busy
// apply must not let the change be rolled back.
func (m *Manager) Keep(snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.store.LoadConfirmation()
	if errors.Is(err, state.ErrNoConfirmation) || (err == nil && c.SnapshotID != snapshotID) {
		return ErrNotAwaiting
	}
	if err != nil {
		return err
	}
	return m.store.ClearConfirmation()
}

// Awaiting is the change currently on probation, if any.
func (m *Manager) Awaiting() (*state.Confirmation, bool) {
	c, err := m.store.LoadConfirmation()
	return c, err == nil
}

// Expiry is what happened to a probation window that ran out.
type Expiry struct {
	state.Confirmation
	RolledBack bool
	// Skipped explains why no rollback was attempted.
	Skipped string
	// BeforeID is the snapshot of the config the rollback replaced, so the
	// operator can put the change back if they simply missed the window.
	BeforeID string
	Health   Health
	Err      error
}

// ExpireDue rolls back a change whose window closed before now. It returns nil
// when nothing is due or another operation holds the lock; the caller polls.
func (m *Manager) ExpireDue(now time.Time) (*Expiry, error) {
	if !m.mu.TryLock() {
		return nil, nil
	}
	defer m.mu.Unlock()
	c, err := m.store.LoadConfirmation()
	if errors.Is(err, state.ErrNoConfirmation) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if now.Before(c.Deadline) {
		return nil, nil
	}
	live, err := m.panel.GetXraySetting()
	if err != nil {
		// Keep the window armed and try again on the next poll.
		return nil, err
	}
	out := &Expiry{Confirmation: *c}
	if !sameJSON(live.Template, c.Applied) {
		out.Skipped = "到期时面板配置已被其他改动替换，没有自动回滚"
		m.disarm()
		return out, nil
	}
	snap, err := m.store.GetSnapshot(c.SnapshotID)
	if err != nil {
		out.Err = err
		m.disarm()
		return out, nil
	}
	before, err := m.store.SaveSnapshot("到期自动回滚前的状态", live.Template)
	if err != nil {
		return nil, err
	}
	out.BeforeID = before.ID
	out.Health, out.Err = m.restore(&panel.XraySetting{Template: snap.Template, OutboundTestURL: live.OutboundTestURL}, m.coreWanted())
	out.RolledBack = out.Err == nil
	// A failed restore is reported once rather than retried every poll: the
	// panel is refusing writes and the operator has to look anyway.
	m.disarm()
	m.epoch.Add(1)
	return out, nil
}
