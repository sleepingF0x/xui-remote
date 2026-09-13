package panel

import (
	"encoding/json"
	"net/url"
	"strconv"
)

// OutboundSub is a remote outbound list. Its outbounds live in the panel's
// database and are merged into the generated config, so they are invisible to
// the config template and unaffected by template snapshots.
type OutboundSub struct {
	Id             int    `json:"id"`
	Remark         string `json:"remark"`
	Url            string `json:"url"`
	Enabled        bool   `json:"enabled"`
	TagPrefix      string `json:"tagPrefix"`
	UserAgent      string `json:"userAgent"`
	UpdateInterval int    `json:"updateInterval"`
	Prepend        bool   `json:"prepend"`
	AllowPrivate   bool   `json:"allowPrivate"`
	AllowInsecure  bool   `json:"allowInsecure"`
	LastUpdated    int64  `json:"lastUpdated"`
	LastError      string `json:"lastError"`
	OutboundCount  int    `json:"outboundCount"`
}

func (c *Client) ListOutboundSubs() ([]OutboundSub, error) {
	obj, err := c.Get("xray/outbound-subs", nil)
	if err != nil {
		return nil, err
	}
	var subs []OutboundSub
	if err := decodeObj(obj, &subs); err != nil {
		return nil, err
	}
	return subs, nil
}

// PreviewOutboundSub fetches and parses a URL without storing anything.
func (c *Client) PreviewOutboundSub(rawURL string) ([]json.RawMessage, error) {
	form := url.Values{}
	form.Set("url", rawURL)
	obj, err := c.Post("xray/outbound-subs/parse", form)
	if err != nil {
		return nil, err
	}
	// The handler wraps the parsed outbounds; accept either shape so a panel
	// version change does not break the preview.
	var wrapped struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := decodeObj(obj, &wrapped); err == nil && wrapped.Outbounds != nil {
		return wrapped.Outbounds, nil
	}
	var direct []json.RawMessage
	if err := decodeObj(obj, &direct); err != nil {
		return nil, err
	}
	return direct, nil
}

func (c *Client) CreateOutboundSub(remark, rawURL, tagPrefix string) (*OutboundSub, error) {
	form := url.Values{}
	form.Set("remark", remark)
	form.Set("url", rawURL)
	setIf(form, "tagPrefix", tagPrefix)
	form.Set("enabled", "true")
	obj, err := c.Post("xray/outbound-subs", form)
	if err != nil {
		return nil, err
	}
	var sub OutboundSub
	if err := decodeObj(obj, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

// SetOutboundSubEnabled re-sends every field: the update handler rebuilds the
// row from the form, so omitting a field clears it.
func (c *Client) SetOutboundSubEnabled(sub OutboundSub, enabled bool) error {
	form := url.Values{}
	form.Set("remark", sub.Remark)
	form.Set("url", sub.Url)
	setIf(form, "tagPrefix", sub.TagPrefix)
	setIf(form, "userAgent", sub.UserAgent)
	form.Set("enabled", strconv.FormatBool(enabled))
	form.Set("updateInterval", strconv.Itoa(sub.UpdateInterval))
	form.Set("prepend", strconv.FormatBool(sub.Prepend))
	form.Set("allowPrivate", strconv.FormatBool(sub.AllowPrivate))
	form.Set("allowInsecure", strconv.FormatBool(sub.AllowInsecure))
	_, err := c.Post("xray/outbound-subs/"+strconv.Itoa(sub.Id), form)
	return err
}

// RefreshOutboundSub re-fetches the list and returns the parsed outbounds. The
// panel only flags xray as needing a restart; it does not restart it.
func (c *Client) RefreshOutboundSub(id int) ([]json.RawMessage, error) {
	obj, err := c.Post("xray/outbound-subs/"+strconv.Itoa(id)+"/refresh", nil)
	if err != nil {
		return nil, err
	}
	var obs []json.RawMessage
	if err := decodeObj(obj, &obs); err != nil {
		return nil, nil
	}
	return obs, nil
}

func (c *Client) DeleteOutboundSub(id int) error {
	_, err := c.Post("xray/outbound-subs/"+strconv.Itoa(id)+"/del", nil)
	return err
}
