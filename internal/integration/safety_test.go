package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sleepingF0x/xui-remote/internal/bot"
	"github.com/sleepingF0x/xui-remote/internal/txn"
)

// withBroken adds an outbound the simulated core cannot start with.
var withBroken = strings.Replace(initial, `{"tag":"C","protocol":"freedom"}`,
	`{"tag":"C","protocol":"freedom"},{"tag":"`+brokenMarker+`","protocol":"freedom"}`, 1)

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(av, bv)
}

func (h *harness) liveJSON() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.live...)
}

func (h *harness) open(chat int64) *bot.Context {
	return h.bot.Open(context.Background(), chat, "config")
}

func TestConfigThatKillsTheCoreIsRestoredAndStaysStaged(t *testing.T) {
	h := newHarness(t, withBroken, nil)
	h.retarget(1, 1, brokenMarker)
	h.apply(1)
	h.contains(1, "提交未生效")
	h.contains(1, "已把面板恢复为提交前的配置，Xray 运行正常")
	h.contains(1, "改动还在暂存区")
	if !sameJSON(t, h.liveJSON(), []byte(withBroken)) {
		t.Fatalf("panel left on the broken draft: %s", h.liveJSON())
	}
	if h.xray != "running" {
		t.Fatalf("core left %s after restore", h.xray)
	}
	if _, err := h.manager.Pending(); err != nil {
		t.Fatalf("draft lost after a failed apply: %v", err)
	}
	if snaps, _ := h.manager.Snapshots(); len(snaps) != 0 {
		t.Fatalf("a commit that never took effect left %d snapshots", len(snaps))
	}
	if _, ok := h.manager.Awaiting(); ok {
		t.Fatal("a failed apply armed a confirmation window")
	}
}

// The panel stores the template before reconciling the core, so a refused
// reconcile answers with an error while the panel already holds the draft.
func TestRefusedReconcileIsNotMistakenForAWebPanelConflict(t *testing.T) {
	var h *harness
	h = newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/xray/update" {
			return false
		}
		_ = r.ParseForm()
		h.mu.Lock()
		first := h.writes == 0
		h.live = json.RawMessage(r.Form.Get("xraySetting"))
		h.writes++
		h.mu.Unlock()
		if first {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "msg": "xray config refused: port 443 in use"})
			return true
		}
		reply(w, nil)
		return true
	})
	h.retarget(1, 1, "C")
	h.apply(1)
	h.contains(1, "提交未生效")
	h.contains(1, "port 443 in use")
	if !sameJSON(t, h.liveJSON(), []byte(initial)) {
		t.Fatalf("refused draft left in the panel: %s", h.liveJSON())
	}
	h.apply(1)
	h.saw(1, "已提交，Xray 运行正常")
}

// An apply whose reply is lost has landed; retrying must record it rather
// than call it a web-panel edit or write and restart again.
func TestLandedApplyWithLostReplyIsAdopted(t *testing.T) {
	var h *harness
	failRead := false
	h = newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/xray/update" && h.writes == 0:
			_ = r.ParseForm()
			h.mu.Lock()
			h.live = json.RawMessage(r.Form.Get("xraySetting"))
			h.writes++
			h.mu.Unlock()
			failRead = true
			http.Error(w, "upstream timeout", http.StatusGatewayTimeout)
			return true
		case r.URL.Path == "/xray/" && failRead:
			failRead = false
			http.Error(w, "upstream timeout", http.StatusGatewayTimeout)
			return true
		}
		return false
	})
	h.retarget(1, 1, "C")
	h.apply(1)
	h.contains(1, "提交结果未知")
	pending, err := h.manager.Pending()
	if err != nil {
		t.Fatal("staging dropped while the outcome was unknown")
	}
	h.apply(1)
	h.saw(1, "上次提交其实已写入面板")
	if h.writes != 1 {
		t.Fatalf("landed draft written again: %d writes", h.writes)
	}
	if _, err := h.manager.Pending(); err == nil {
		t.Fatal("adopted apply left the draft staged")
	}
	snaps, _ := h.manager.Snapshots()
	if len(snaps) != 1 || !sameJSON(t, snaps[0].Template, pending.Base) {
		t.Fatalf("snapshot should hold the pre-change config: %+v", snaps)
	}
}

func TestUnkeptChangeRollsBackAndCanBeRestored(t *testing.T) {
	h := newHarness(t, initial, nil, txn.WithConfirmWindow(time.Minute))
	h.retarget(1, 1, "C")
	h.apply(1)
	h.saw(1, "请在")
	if button(h.last(1), "✅ 保留改动") == "" {
		t.Fatal("verification report lacks the keep button")
	}
	applied := h.liveJSON()

	h.config.ExpireDue(h.open, time.Now().Add(30*time.Second))
	if !sameJSON(t, h.liveJSON(), applied) {
		t.Fatal("rolled back before the deadline")
	}
	h.config.ExpireDue(h.open, time.Now().Add(2*time.Minute))
	h.contains(1, "已自动回滚")
	if !sameJSON(t, h.liveJSON(), []byte(initial)) {
		t.Fatalf("expired change not rolled back: %s", h.liveJSON())
	}
	if _, ok := h.manager.Awaiting(); ok {
		t.Fatal("window still armed after expiry")
	}

	h.press(1, "恢复这次改动")
	h.press(1, "确认回滚")
	h.contains(1, "已回滚")
	if !sameJSON(t, h.liveJSON(), applied) {
		t.Fatal("missed change could not be restored")
	}
}

func TestKeptChangeSurvivesTheDeadline(t *testing.T) {
	h := newHarness(t, initial, nil, txn.WithConfirmWindow(time.Minute))
	h.retarget(1, 1, "C")
	h.apply(1)
	h.press(1, "✅ 保留改动")
	h.contains(1, "已保留改动")
	applied := h.liveJSON()
	h.config.ExpireDue(h.open, time.Now().Add(2*time.Minute))
	if !sameJSON(t, h.liveJSON(), applied) {
		t.Fatal("kept change was rolled back")
	}
	h.press(1, "← 主菜单")
}

func TestExpiryNeverUndoesALaterEdit(t *testing.T) {
	h := newHarness(t, initial, nil, txn.WithConfirmWindow(time.Minute))
	h.retarget(1, 1, "C")
	h.apply(1)
	later := strings.Replace(string(h.liveJSON()), "domain:c.test", "domain:web.test", 1)
	h.mu.Lock()
	h.live = json.RawMessage(later)
	h.mu.Unlock()
	h.config.ExpireDue(h.open, time.Now().Add(2*time.Minute))
	h.contains(1, "没有自动回滚")
	if !sameJSON(t, h.liveJSON(), []byte(later)) {
		t.Fatal("expiry overwrote a later web-panel edit")
	}
}

func TestRollbackStartsACoreThatIsDown(t *testing.T) {
	h := newHarness(t, initial, nil)
	h.retarget(1, 1, "C")
	h.apply(1)
	h.mu.Lock()
	h.xray = "error"
	h.mu.Unlock()
	h.send(1, "/rollback")
	h.press(1, "回滚到 "+mustFirstSnapshot(t, h))
	h.press(1, "确认回滚")
	h.contains(1, "Xray 运行正常")
	if h.restarts == 0 {
		t.Fatal("rollback left a crashed core down")
	}
}

func TestStoppedCoreIsNotProbedOrPutOnProbation(t *testing.T) {
	probed := false
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/xray/routeTest" {
			probed = true
		}
		return false
	}, txn.WithConfirmWindow(time.Minute))
	h.xray = "stop"
	h.retarget(1, 1, "C")
	h.apply(1)
	h.contains(1, "停止状态")
	if probed {
		t.Fatal("verification probed a stopped core")
	}
	if _, ok := h.manager.Awaiting(); ok {
		t.Fatal("a stopped core was put on probation")
	}
	if h.xray != "stop" || h.restarts != 0 {
		t.Fatal("apply started a core that was stopped on purpose")
	}
}

func mustFirstSnapshot(t *testing.T, h *harness) string {
	t.Helper()
	snaps, err := h.manager.Snapshots()
	if err != nil || len(snaps) == 0 {
		t.Fatalf("no snapshots: %v", err)
	}
	return snaps[0].ID
}

// A running core's errorMsg is a refusal the panel remembers until the next
// restart, so the status page must not present it as Xray failing.
func TestStatusDoesNotCallAHeldBackRecordAnXrayError(t *testing.T) {
	h := newHarness(t, initial, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/server/status" {
			return false
		}
		reply(w, map[string]any{"xray": map[string]any{"state": "running", "errorMsg": "config refused: port 443", "version": "test"}})
		return true
	})
	h.send(1, "/status")
	h.contains(1, "面板记录了一次被拒绝的配置")
	if strings.Contains(h.last(1).Text, "Xray 报错") {
		t.Fatal("a running core is reported as failing")
	}
}
