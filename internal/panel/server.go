package panel

import "encoding/json"

// Status is the subset of the panel's /server/status payload the bot renders.
type Status struct {
	Cpu float64 `json:"cpu"`
	Mem struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"mem"`
	Xray struct {
		State    string `json:"state"`
		ErrorMsg string `json:"errorMsg"`
		Version  string `json:"version"`
	} `json:"xray"`
	PanelVersion string `json:"panelVersion"`
	Uptime       uint64 `json:"uptime"`
	TcpCount     int    `json:"tcpCount"`
}

func (c *Client) Status() (*Status, error) {
	obj, err := c.Get("server/status", nil)
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(obj, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// XrayResult is the core's own error output — the only useful diagnostic when
// a config change leaves xray unable to start.
func (c *Client) XrayResult() (string, error) {
	obj, err := c.Get("xray/getXrayResult", nil)
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal(obj, &out); err != nil {
		return string(obj), nil
	}
	return out, nil
}

func (c *Client) RestartXray() error {
	_, err := c.Post("server/restartXrayService", nil)
	return err
}
