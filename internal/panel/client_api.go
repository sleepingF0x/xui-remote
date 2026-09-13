package panel

import (
	"net/url"
	"strconv"
)

// ClientTraffic is the usage counter row behind a client identifier.
type ClientTraffic struct {
	Enable     bool   `json:"enable"`
	Email      string `json:"email"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	Total      int64  `json:"total"`
	ExpiryTime int64  `json:"expiryTime"`
	LastOnline int64  `json:"lastOnline"`
}

// ClientSlim is one row of the paged client list.
type ClientSlim struct {
	Email      string         `json:"email"`
	SubID      string         `json:"subId"`
	Enable     bool           `json:"enable"`
	TotalGB    int64          `json:"totalGB"`
	ExpiryTime int64          `json:"expiryTime"`
	Comment    string         `json:"comment,omitempty"`
	Group      string         `json:"group,omitempty"`
	InboundIds []int          `json:"inboundIds"`
	Traffic    *ClientTraffic `json:"traffic,omitempty"`
}

type ClientPage struct {
	Items    []ClientSlim `json:"items"`
	Total    int          `json:"total"`
	Filtered int          `json:"filtered"`
}

// SearchClients pages the client list; search matches the identifier and is
// the only filter the bot needs.
func (c *Client) SearchClients(search string, page, pageSize int) (*ClientPage, error) {
	q := url.Values{}
	setIf(q, "search", search)
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if pageSize > 0 {
		q.Set("pageSize", strconv.Itoa(pageSize))
	}
	obj, err := c.Get("clients/list/paged", q)
	if err != nil {
		return nil, err
	}
	var out ClientPage
	if err := decodeObj(obj, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ClientTraffic(email string) (*ClientTraffic, error) {
	obj, err := c.Get("clients/traffic/"+url.PathEscape(email), nil)
	if err != nil {
		return nil, err
	}
	var t ClientTraffic
	if err := decodeObj(obj, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ClientLinks returns the client's individual share links, one per inbound.
func (c *Client) ClientLinks(email string) ([]string, error) {
	obj, err := c.Get("clients/links/"+url.PathEscape(email), nil)
	if err != nil {
		return nil, err
	}
	var links []string
	if err := decodeObj(obj, &links); err != nil {
		return nil, err
	}
	return links, nil
}

// SubLinks returns the links a subscription id resolves to. The subscription
// URL itself is built by the panel's settings, not by this call.
func (c *Client) SubLinks(subID string) ([]string, error) {
	obj, err := c.Get("clients/subLinks/"+url.PathEscape(subID), nil)
	if err != nil {
		return nil, err
	}
	var links []string
	if err := decodeObj(obj, &links); err != nil {
		return nil, err
	}
	return links, nil
}

// PanelSettings is the subset of /setting/defaultSettings the bot needs to
// build a subscription URL the same way the panel's own pages do.
type PanelSettings struct {
	SubEnable bool   `json:"subEnable"`
	SubURI    string `json:"subURI"`
	SubDomain string `json:"subDomain"`
}

func (c *Client) PanelSettings() (*PanelSettings, error) {
	obj, err := c.Post("setting/defaultSettings", nil)
	if err != nil {
		return nil, err
	}
	var s PanelSettings
	if err := decodeObj(obj, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
