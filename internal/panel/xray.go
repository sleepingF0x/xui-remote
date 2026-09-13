package panel

import (
	"encoding/json"
	"net/url"
	"strconv"
)

// XraySetting is the panel's Xray configuration template plus the one sibling
// field /xray/update also rewrites; both must be sent back on every save.
type XraySetting struct {
	Template        json.RawMessage
	OutboundTestURL string
}

func (c *Client) GetXraySetting() (*XraySetting, error) {
	obj, err := c.Post("xray/", nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		XraySetting     json.RawMessage `json:"xraySetting"`
		OutboundTestURL string          `json:"outboundTestUrl"`
	}
	if err := json.Unmarshal(obj, &body); err != nil {
		return nil, err
	}
	return &XraySetting{Template: body.XraySetting, OutboundTestURL: body.OutboundTestURL}, nil
}

// UpdateXraySetting replaces the whole template and restarts a running core.
// Omitting outboundTestUrl silently resets it, so callers pass back what they read.
func (c *Client) UpdateXraySetting(s *XraySetting) error {
	form := url.Values{}
	form.Set("xraySetting", string(s.Template))
	form.Set("outboundTestUrl", s.OutboundTestURL)
	_, err := c.Post("xray/update", form)
	return err
}

type RouteTestRequest struct {
	InboundTag string
	Domain     string
	IP         string
	Port       int
	Network    string
	Protocol   string
	Email      string
}

// RouteTestResult mirrors xray.RouteTestResult: Matched is false when no rule
// applied, which means the traffic falls through to the default outbound.
type RouteTestResult struct {
	Matched     bool     `json:"matched"`
	OutboundTag string   `json:"outboundTag"`
	GroupTags   []string `json:"groupTags,omitempty"`
}

func (c *Client) RouteTest(req RouteTestRequest) (*RouteTestResult, error) {
	form := url.Values{}
	setIf(form, "inboundTag", req.InboundTag)
	setIf(form, "domain", req.Domain)
	setIf(form, "ip", req.IP)
	setIf(form, "network", req.Network)
	setIf(form, "protocol", req.Protocol)
	setIf(form, "email", req.Email)
	if req.Port > 0 {
		form.Set("port", strconv.Itoa(req.Port))
	}
	obj, err := c.Post("xray/routeTest", form)
	if err != nil {
		return nil, err
	}
	var res RouteTestResult
	if err := json.Unmarshal(obj, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type OutboundTestResult struct {
	Tag        string `json:"tag,omitempty"`
	Success    bool   `json:"success"`
	Delay      int64  `json:"delay"`
	Error      string `json:"error,omitempty"`
	Mode       string `json:"mode,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

// TestOutbounds probes the given outbound definitions. mode is "tcp" (direct
// dial), "real" (real-delay) or "" for the default HTTP probe.
func (c *Client) TestOutbounds(outbounds []json.RawMessage, allOutbounds json.RawMessage, mode string) ([]OutboundTestResult, error) {
	payload, err := json.Marshal(outbounds)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("outbounds", string(payload))
	setIf(form, "mode", mode)
	if len(allOutbounds) > 0 {
		form.Set("allOutbounds", string(allOutbounds))
	}
	obj, err := c.Post("xray/testOutbounds", form)
	if err != nil {
		return nil, err
	}
	var res []OutboundTestResult
	if err := json.Unmarshal(obj, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// OverrideBalancer pins a balancer to one outbound at runtime; an empty target
// clears the override. Runtime only — it is not written to the template.
func (c *Client) OverrideBalancer(tag, target string) error {
	form := url.Values{}
	form.Set("tag", tag)
	form.Set("target", target)
	_, err := c.Post("xray/balancerOverride", form)
	return err
}

type GeoEntryPage struct {
	Total   int      `json:"total"`
	Entries []string `json:"entries"`
}

// GeodataEntries lists a category's domains or CIDRs; query filters by substring.
func (c *Client) GeodataEntries(file, code, query string, offset, limit int) (*GeoEntryPage, error) {
	q := url.Values{}
	setIf(q, "file", file)
	q.Set("code", code)
	setIf(q, "q", query)
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	obj, err := c.Get("xray/geodata/entries", q)
	if err != nil {
		return nil, err
	}
	var page GeoEntryPage
	if err := json.Unmarshal(obj, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func setIf(v url.Values, key, value string) {
	if value != "" {
		v.Set(key, value)
	}
}
