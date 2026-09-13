package panel

import (
	"net/url"
	"strconv"
)

// InboundSlim is the list row; the slim endpoint strips settings, so this is
// a view model only and must never be posted back as an update.
type InboundSlim struct {
	Id       int    `json:"id"`
	Tag      string `json:"tag"`
	Remark   string `json:"remark"`
	Listen   string `json:"listen"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Enable   bool   `json:"enable"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
}

func (c *Client) ListInbounds() ([]InboundSlim, error) {
	obj, err := c.Get("inbounds/list/slim", nil)
	if err != nil {
		return nil, err
	}
	var out []InboundSlim
	if err := decodeObj(obj, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetInboundEnable toggles one inbound. The panel restarts xray itself when
// the change requires it.
func (c *Client) SetInboundEnable(id int, enable bool) error {
	form := url.Values{}
	form.Set("enable", strconv.FormatBool(enable))
	_, err := c.Post("inbounds/setEnable/"+strconv.Itoa(id), form)
	return err
}
