package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClients stands in for the panel's client store. It keeps records in the
// panel's ClientRecord shape and applies writes the way the panel does: an
// update replaces every non-credential field with what it is sent.
type fakeClients struct {
	mu      sync.Mutex
	records map[string]map[string]any
	inbound map[string][]int
	nextID  int
	resets  int
}

func newFakeClients() *fakeClients {
	f := &fakeClients{records: map[string]map[string]any{}, inbound: map[string][]int{}}
	f.put(map[string]any{"email": "alice", "uuid": "uuid-alice", "subId": "sub-alice", "enable": true,
		"totalGB": float64(10 << 30), "expiryTime": float64(0), "limitIp": float64(2), "comment": "friend",
		"flow": "xtls-rprx-vision", "createdAt": float64(1700)}, []int{1})
	return f
}

func (f *fakeClients) put(rec map[string]any, inbounds []int) {
	f.nextID++
	rec["id"] = float64(f.nextID)
	f.records[rec["email"].(string)] = rec
	f.inbound[rec["email"].(string)] = inbounds
}

func (f *fakeClients) get(email string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.records[email]
}

func (f *fakeClients) serve(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	f.mu.Lock()
	defer f.mu.Unlock()
	fail := func(msg string) bool {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "msg": msg})
		return true
	}
	body := func(v any) { raw, _ := io.ReadAll(r.Body); _ = json.Unmarshal(raw, v) }
	switch {
	case path == "/inbounds/list/slim":
		reply(w, []any{
			map[string]any{"id": 1, "remark": "main", "protocol": "vless", "port": 443, "enable": true},
			map[string]any{"id": 2, "remark": "backup", "protocol": "trojan", "port": 8443, "enable": true},
		})
	case path == "/clients/list/paged":
		var items []any
		for _, rec := range f.records {
			items = append(items, map[string]any{"email": rec["email"], "subId": rec["subId"], "enable": rec["enable"],
				"totalGB": rec["totalGB"], "expiryTime": rec["expiryTime"]})
		}
		reply(w, map[string]any{"items": items, "total": len(items), "filtered": len(items)})
	case strings.HasPrefix(path, "/clients/get/"):
		email := strings.TrimPrefix(path, "/clients/get/")
		rec, ok := f.records[email]
		if !ok {
			return fail("record not found")
		}
		reply(w, map[string]any{"client": rec, "inboundIds": f.inbound[email]})
	case strings.HasPrefix(path, "/clients/traffic/"):
		rec, ok := f.records[strings.TrimPrefix(path, "/clients/traffic/")]
		if !ok {
			return fail("record not found")
		}
		reply(w, map[string]any{"email": rec["email"], "enable": rec["enable"], "up": float64(1 << 30), "down": float64(1 << 30),
			"total": rec["totalGB"], "expiryTime": rec["expiryTime"]})
	case path == "/clients/add":
		var req struct {
			Client     map[string]any `json:"client"`
			InboundIds []int          `json:"inboundIds"`
		}
		body(&req)
		email, _ := req.Client["email"].(string)
		if _, taken := f.records[email]; taken {
			return fail("email already in use: " + email)
		}
		if len(req.InboundIds) == 0 {
			return fail("at least one inbound is required")
		}
		// Panel side: the client's id is the record's uuid; missing ones are minted.
		rec := map[string]any{}
		for k, v := range req.Client {
			rec[k] = v
		}
		rec["uuid"], _ = req.Client["id"].(string)
		if rec["uuid"] == "" {
			rec["uuid"] = "uuid-new-" + email
		}
		if s, _ := rec["subId"].(string); s == "" {
			rec["subId"] = "sub-new-" + email
		}
		f.put(rec, req.InboundIds)
		reply(w, nil)
	case strings.HasPrefix(path, "/clients/update/"):
		email := strings.TrimPrefix(path, "/clients/update/")
		old, ok := f.records[email]
		if !ok {
			return fail("record not found")
		}
		var client map[string]any
		body(&client)
		if _, isNumber := client["id"].(float64); isNumber {
			return fail("json: cannot unmarshal number into Go struct field Client.id of type string")
		}
		rec := map[string]any{"id": old["id"], "uuid": old["uuid"], "createdAt": old["createdAt"]}
		if id, _ := client["id"].(string); id != "" {
			rec["uuid"] = id
		}
		for k, v := range client {
			if k != "id" && k != "created_at" {
				rec[k] = v
			}
		}
		if s, _ := rec["subId"].(string); s == "" {
			rec["subId"] = old["subId"]
		}
		f.records[email] = rec
		reply(w, nil)
	case path == "/clients/bulkEnable", path == "/clients/bulkDisable":
		var req struct {
			Emails []string `json:"emails"`
		}
		body(&req)
		for _, e := range req.Emails {
			if rec, ok := f.records[e]; ok {
				rec["enable"] = path == "/clients/bulkEnable"
			}
		}
		reply(w, map[string]any{"adjusted": len(req.Emails)})
	case strings.HasPrefix(path, "/clients/resetTraffic/"):
		f.resets++
		reply(w, nil)
	case strings.HasPrefix(path, "/clients/del/"):
		email := strings.TrimPrefix(path, "/clients/del/")
		delete(f.records, email)
		delete(f.inbound, email)
		reply(w, nil)
	default:
		return false
	}
	return true
}

func (h *harness) openClient(chat int64, email string) {
	h.t.Helper()
	h.send(chat, "👥 客户端")
	h.press(chat, email)
}

// An update replaces every non-credential field, so changing the quota must not
// clear the expiry, IP limit, comment or flow, nor rotate the credentials.
func TestQuotaAndExpiryEditsKeepEveryOtherField(t *testing.T) {
	f := newFakeClients()
	h := newHarness(t, initial, f.serve)
	h.openClient(1, "alice")
	h.press(1, "📦 流量额度")
	h.press(1, "100 GB")
	h.contains(1, "流量额度已改为 100 GB")

	rec := f.get("alice")
	if rec["totalGB"] != float64(100<<30) {
		t.Fatalf("quota not written: %v", rec["totalGB"])
	}
	for k, want := range map[string]any{"uuid": "uuid-alice", "subId": "sub-alice", "limitIp": float64(2), "comment": "friend", "flow": "xtls-rprx-vision", "expiryTime": float64(0), "enable": true} {
		if rec[k] != want {
			t.Fatalf("%s changed to %v by a quota edit", k, rec[k])
		}
	}

	h.press(1, "⏳ 有效期")
	h.press(1, "+30 天")
	exp, _ := f.get("alice")["expiryTime"].(float64)
	if days := time.Until(time.UnixMilli(int64(exp))).Hours() / 24; days < 29.9 || days > 30.1 {
		t.Fatalf("a never-expiring client renewed by 30 days should expire in 30 days, got %.1f", days)
	}
	if f.get("alice")["totalGB"] != float64(100<<30) {
		t.Fatal("the expiry edit cleared the quota")
	}
}

// Renewing a lapsed client adds usable days from today, not from the old date.
func TestRenewingALapsedClientCountsFromToday(t *testing.T) {
	f := newFakeClients()
	f.records["alice"]["expiryTime"] = float64(time.Now().AddDate(0, 0, -10).UnixMilli())
	h := newHarness(t, initial, f.serve)
	h.openClient(1, "alice")
	h.press(1, "⏳ 有效期")
	h.contains(1, "续期从今天算起")
	h.press(1, "+30 天")
	exp, _ := f.get("alice")["expiryTime"].(float64)
	if days := time.Until(time.UnixMilli(int64(exp))).Hours() / 24; days < 29.9 {
		t.Fatalf("lapsed client got only %.1f usable days", days)
	}
}

func TestNewClientWizardCreatesOnTheChosenInbounds(t *testing.T) {
	f := newFakeClients()
	h := newHarness(t, initial, f.serve)
	h.send(1, "👥 客户端")
	h.press(1, "+ 新增客户端")
	h.send(1, "alice")
	h.contains(1, "已经存在")
	h.send(1, "bob smith")
	h.contains(1, "不能包含空格")
	h.send(1, "bob")
	h.contains(1, "挂到哪些入站")
	h.press(1, "✅ backup · trojan:8443")
	h.press(1, "下一步 →")
	h.press(1, "50 GB")
	h.press(1, "自定义…")
	h.send(1, "45")
	h.contains(1, "确认新增客户端")
	h.contains(1, "main")
	h.press(1, "✅ 创建")
	h.contains(1, "已创建")

	rec := f.get("bob")
	if rec == nil {
		t.Fatal("client not created")
	}
	if rec["totalGB"] != float64(50<<30) || rec["enable"] != true {
		t.Fatalf("wrong client: %v", rec)
	}
	if got := f.inbound["bob"]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("attached to %v, want only the inbound left selected", got)
	}
	exp, _ := rec["expiryTime"].(float64)
	if days := time.Until(time.UnixMilli(int64(exp))).Hours() / 24; days < 44.9 || days > 45.1 {
		t.Fatalf("custom 45 days became %.1f", days)
	}
}

// Deleting is the one irreversible client write; undo must restore the same
// credentials and subscription id so links already handed out keep working.
func TestDeletedClientCanBeRestoredWithItsCredentials(t *testing.T) {
	f := newFakeClients()
	h := newHarness(t, initial, f.serve)
	h.openClient(1, "alice")
	h.press(1, "🗑 删除")
	h.press(1, "🗑 确认删除")
	h.contains(1, "已删除")
	if f.get("alice") != nil {
		t.Fatal("not deleted")
	}
	h.press(1, "↩️ 撤销删除")
	h.contains(1, "已恢复")
	rec := f.get("alice")
	for k, want := range map[string]any{"uuid": "uuid-alice", "subId": "sub-alice", "comment": "friend", "limitIp": float64(2)} {
		if rec[k] != want {
			t.Fatalf("undo restored %s as %v", k, rec[k])
		}
	}
	if got := f.inbound["alice"]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("undo attached to %v", got)
	}
	if _, ok := h.store.DeletedClient("alice"); ok {
		t.Fatal("undo record left behind")
	}
}

func TestToggleAndResetActOnTheClient(t *testing.T) {
	f := newFakeClients()
	h := newHarness(t, initial, f.serve)
	h.openClient(1, "alice")
	h.press(1, "⏸ 停用")
	h.contains(1, "已停用")
	if f.get("alice")["enable"] != false {
		t.Fatal("not disabled")
	}
	h.press(1, "▶️ 启用")
	if f.get("alice")["enable"] != true {
		t.Fatal("not re-enabled")
	}
	h.press(1, "🔄 清零流量")
	h.press(1, "确认清零")
	h.contains(1, "已清零")
	if f.resets != 1 {
		t.Fatalf("%d resets", f.resets)
	}
}
