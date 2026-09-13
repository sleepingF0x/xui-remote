// Package panel is a REST client for a 3x-ui panel's /panel/api surface.
package panel

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one panel as an admin-scope API token.
type Client struct {
	baseURL    string
	token      string
	publicHost string
	http       *http.Client
}

type Options struct {
	BaseURL     string
	Token       string
	PublicHost  string
	Timeout     time.Duration
	InsecureTLS bool
}

func New(o Options) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if o.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		baseURL:    strings.TrimSuffix(o.BaseURL, "/") + "/",
		token:      o.Token,
		publicHost: o.PublicHost,
		http:       &http.Client{Timeout: o.Timeout, Transport: transport},
	}
}

// envelope is the panel's uniform response shape (entity.Msg).
type envelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// APIError is a call the panel understood and refused, as opposed to a
// transport failure; callers can surface Msg to the operator verbatim.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("panel returned HTTP %d", e.Status)
	}
	return e.Msg
}

// Get calls an endpoint with query parameters and returns its obj field.
func (c *Client) Get(path string, query url.Values) (json.RawMessage, error) {
	full := c.baseURL + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}
	return c.send(req)
}

// Post calls an endpoint with a form-encoded body — the panel's handlers read
// parameters with c.PostForm, so JSON bodies are silently ignored.
func (c *Client) Post(path string, form url.Values) (json.RawMessage, error) {
	body := strings.NewReader(form.Encode())
	req, err := http.NewRequest(http.MethodPost, c.baseURL+strings.TrimPrefix(path, "/"), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.send(req)
}

// PostJSON is for the few handlers that bind a JSON body instead of a form.
func (c *Client) PostJSON(path string, payload any) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+strings.TrimPrefix(path, "/"), strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.send(req)
}

func (c *Client) send(req *http.Request) (json.RawMessage, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	// Without this the panel answers an unauthenticated call with 404 instead
	// of 401, which reads as a wrong URL rather than a bad token.
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	// resolveHost falls back to the request Host, so share links generated for
	// a loopback or tailnet dial would carry an address clients cannot reach.
	if c.publicHost != "" {
		req.Host = c.publicHost
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &APIError{Status: resp.StatusCode, Msg: "认证失败：检查 api_token 是否有效且为 admin 权限"}
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &APIError{Status: resp.StatusCode, Msg: fmt.Sprintf("响应不是 JSON（HTTP %d）：%.200s", resp.StatusCode, raw)}
	}
	if !env.Success {
		return nil, &APIError{Status: resp.StatusCode, Msg: env.Msg}
	}
	return env.Obj, nil
}
