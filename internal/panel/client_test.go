package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
