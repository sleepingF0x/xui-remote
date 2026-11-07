package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Options{
		BaseURL:    srv.URL + "/panel/panel/api/",
		Token:      "tok123",
		PublicHost: "public.example.com",
		Timeout:    5 * time.Second,
	})
}

func TestRequestCarriesAuthAndHostOverride(t *testing.T) {
	var gotAuth, gotHost, gotPath, gotXHR string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotHost = r.Host
		gotPath = r.URL.Path
		gotXHR = r.Header.Get("X-Requested-With")
		_ = json.NewEncoder(w).Encode(envelope{Success: true, Obj: json.RawMessage(`"ok"`)})
	})

	if _, err := c.Get("server/status", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer tok123" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	// resolveHost falls back to the request Host, so links generated for a
	// loopback or tailnet dial would otherwise be unreachable for clients.
	if gotHost != "public.example.com" {
		t.Fatalf("Host = %q, want the configured public host", gotHost)
	}
	if gotPath != "/panel/panel/api/server/status" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotXHR != "XMLHttpRequest" {
		t.Fatalf("X-Requested-With = %q", gotXHR)
	}
}

func TestPostSendsFormEncodedBody(t *testing.T) {
	var got url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		_ = json.NewEncoder(w).Encode(envelope{Success: true})
	})

	form := url.Values{}
	form.Set("domain", "example.com")
	form.Set("port", "443")
	if _, err := c.Post("xray/routeTest", form); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got.Get("domain") != "example.com" || got.Get("port") != "443" {
		t.Fatalf("form = %v", got)
	}
}

// The panel answers a refused call with HTTP 200 and success:false, so the
// status code alone never tells the caller whether the call worked.
func TestSuccessFalseSurfacesPanelMessage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope{Success: false, Msg: "invalid port"})
	})
	_, err := c.Post("xray/routeTest", nil)
	if err == nil {
		t.Fatal("want an error for success:false")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type = %T, want *APIError", err)
	}
	if apiErr.Msg != "invalid port" {
		t.Fatalf("Msg = %q, want the panel's own message", apiErr.Msg)
	}
}

func TestUnauthorizedIsReportedAsAuthFailure(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		})
		_, err := c.Get("server/status", nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("HTTP %d: err type = %T, want *APIError", code, err)
		}
		if apiErr.Status != code {
			t.Fatalf("Status = %d, want %d", apiErr.Status, code)
		}
	}
}

// A wrong base path reaches the SPA and returns HTML; the error has to say so
// rather than failing with an opaque JSON parse error.
func TestNonJSONResponseIsReportedWithBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html>not the api</html>"))
	})
	_, err := c.Get("server/status", nil)
	if err == nil {
		t.Fatal("want an error for a non-JSON body")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type = %T", err)
	}
	if apiErr.Msg == "" {
		t.Fatal("error must quote the body so a wrong base_url is diagnosable")
	}
}

func TestGetXraySettingExtractsTemplateAndTestURL(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope{Success: true, Obj: json.RawMessage(
			`{"xraySetting":{"routing":{"rules":[]}},"outboundTestUrl":"https://probe.example/204"}`)})
	})
	got, err := c.GetXraySetting()
	if err != nil {
		t.Fatalf("GetXraySetting: %v", err)
	}
	if got.OutboundTestURL != "https://probe.example/204" {
		t.Fatalf("OutboundTestURL = %q", got.OutboundTestURL)
	}
	var tpl map[string]any
	if err := json.Unmarshal(got.Template, &tpl); err != nil {
		t.Fatalf("template is not an object: %v", err)
	}
	if _, ok := tpl["routing"]; !ok {
		t.Fatalf("template lost its routing section: %s", got.Template)
	}
}

// Omitting outboundTestUrl makes the panel reset it to its default, so the
// update must always send back what the read returned.
func TestUpdateXraySettingResendsOutboundTestURL(t *testing.T) {
	var got url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_ = json.NewEncoder(w).Encode(envelope{Success: true})
	})
	err := c.UpdateXraySetting(&XraySetting{
		Template:        json.RawMessage(`{"a":1}`),
		OutboundTestURL: "https://probe.example/204",
	})
	if err != nil {
		t.Fatalf("UpdateXraySetting: %v", err)
	}
	if got.Get("xraySetting") != `{"a":1}` {
		t.Fatalf("xraySetting = %q", got.Get("xraySetting"))
	}
	if got.Get("outboundTestUrl") != "https://probe.example/204" {
		t.Fatalf("outboundTestUrl = %q, want it echoed back", got.Get("outboundTestUrl"))
	}
}

func TestRouteTestOmitsEmptyFields(t *testing.T) {
	var got url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_ = json.NewEncoder(w).Encode(envelope{Success: true, Obj: json.RawMessage(
			`{"matched":true,"outboundTag":"proxy-hk"}`)})
	})
	res, err := c.RouteTest(RouteTestRequest{Domain: "example.com", Port: 443, Network: "tcp"})
	if err != nil {
		t.Fatalf("RouteTest: %v", err)
	}
	if !res.Matched || res.OutboundTag != "proxy-hk" {
		t.Fatalf("result = %+v", res)
	}
	if _, present := got["ip"]; present {
		t.Fatal("an empty ip must not be sent; the panel rejects blank values it is given")
	}
	if _, present := got["email"]; present {
		t.Fatal("an empty email must not be sent")
	}
}

// 3.7.0 serializes /xray/'s obj as a JSON string whose contents are the real
// payload, while its other endpoints return a plain object. The decoder has to
// accept both or the bot breaks against one panel version or the other.
func TestDecodeObjAcceptsDoubleEncodedPayloads(t *testing.T) {
	type payload struct {
		Tag string `json:"tag"`
		N   int    `json:"n"`
	}
	tests := []struct {
		name string
		raw  string
	}{
		{"plain object", `{"tag":"hk","n":3}`},
		{"double encoded", `"{\"tag\":\"hk\",\"n\":3}"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got payload
			if err := decodeObj(json.RawMessage(tt.raw), &got); err != nil {
				t.Fatalf("decodeObj: %v", err)
			}
			if got.Tag != "hk" || got.N != 3 {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestDecodeObjReportsGenuinelyBrokenPayloads(t *testing.T) {
	var got struct{ Tag string }
	if err := decodeObj(json.RawMessage(`{"tag":`), &got); err == nil {
		t.Fatal("truncated JSON must still error")
	}
	if err := decodeObj(json.RawMessage(`"not json inside"`), &got); err == nil {
		t.Fatal("a string that is not encoded JSON must still error")
	}
}

func TestGetXraySettingHandlesTheDoubleEncodedEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":` +
			`"{\"xraySetting\":{\"routing\":{\"rules\":[]}},\"outboundTestUrl\":\"https://probe/204\"}"}`))
	})
	got, err := c.GetXraySetting()
	if err != nil {
		t.Fatalf("GetXraySetting: %v", err)
	}
	if got.OutboundTestURL != "https://probe/204" {
		t.Fatalf("OutboundTestURL = %q", got.OutboundTestURL)
	}
	var tpl map[string]any
	if err := json.Unmarshal(got.Template, &tpl); err != nil {
		t.Fatalf("template unusable: %v", err)
	}
	if _, ok := tpl["routing"]; !ok {
		t.Fatalf("template lost routing: %s", got.Template)
	}
}

// The update endpoint takes the client shape, not the stored record: sending
// the record's numeric id as the client id would fail to decode, and dropping
// the UUID would make the panel mint a new one and break every share link.
func TestRecordConvertsToTheClientShapeTheWriteEndpointsTake(t *testing.T) {
	var rec map[string]any
	_ = json.Unmarshal([]byte(`{"id":7,"uuid":"u-1","email":"a@x","subId":"s1","totalGB":1073741824,
"expiryTime":0,"enable":true,"comment":"keep me","limitIp":2,"createdAt":1700,"updatedAt":1800,
"allowedIPs":"10.0.0.2/32, 10.0.0.3/32","reverse":"","keepAlive":0,"flow":"xtls-rprx-vision","futureField":"x"}`), &rec)
	got := (&ClientDetail{Record: rec}).AsClient()
	want := map[string]any{
		"id": "u-1", "email": "a@x", "subId": "s1", "totalGB": float64(1073741824), "expiryTime": float64(0),
		"enable": true, "comment": "keep me", "limitIp": float64(2), "created_at": int64(1700),
		"allowedIPs": []string{"10.0.0.2/32", "10.0.0.3/32"}, "flow": "xtls-rprx-vision", "futureField": "x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AsClient:\n got %#v\nwant %#v", got, want)
	}
}
