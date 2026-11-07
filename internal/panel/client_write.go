package panel

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ClientDetail is the panel's full record of one client and the inbounds it is
// attached to. Record keeps the panel's own field set as-is so that writing it
// back cannot drop a field this bot does not know about.
type ClientDetail struct {
	Record     map[string]any
	InboundIds []int
}

func (d *ClientDetail) String(key string) string { s, _ := d.Record[key].(string); return s }

func (d *ClientDetail) Int(key string) int64 {
	switch v := d.Record[key].(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func (d *ClientDetail) Bool(key string) bool { b, _ := d.Record[key].(bool); return b }

func (c *Client) GetClient(email string) (*ClientDetail, error) {
	obj, err := c.Get("clients/get/"+url.PathEscape(email), nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Client     map[string]any `json:"client"`
		InboundIds []int          `json:"inboundIds"`
	}
	if err := decodeObj(obj, &body); err != nil {
		return nil, err
	}
	if body.Client == nil {
		return nil, fmt.Errorf("找不到客户端 %s", email)
	}
	return &ClientDetail{Record: body.Client, InboundIds: body.InboundIds}, nil
}

// AsClient converts the stored record into the shape /clients/add and
// /clients/update accept, mirroring the panel's ClientRecord.ToClient. The two
// differ in a handful of fields; everything else passes through by name.
func (d *ClientDetail) AsClient() map[string]any {
	out := make(map[string]any, len(d.Record))
	for k, v := range d.Record {
		out[k] = v
	}
	// The record's id is the database row; the client's id is its UUID.
	delete(out, "id")
	delete(out, "uuid")
	if uuid := d.String("uuid"); uuid != "" {
		out["id"] = uuid
	}
	delete(out, "createdAt")
	delete(out, "updatedAt")
	if n := d.Int("createdAt"); n > 0 {
		out["created_at"] = n
	}
	delete(out, "allowedIPs")
	if ips := strings.TrimSpace(d.String("allowedIPs")); ips != "" {
		var list []string
		for _, ip := range strings.Split(ips, ",") {
			if ip = strings.TrimSpace(ip); ip != "" {
				list = append(list, ip)
			}
		}
		out["allowedIPs"] = list
	}
	delete(out, "reverse")
	if rev := strings.TrimSpace(d.String("reverse")); rev != "" {
		var obj map[string]any
		if json.Unmarshal([]byte(rev), &obj) == nil {
			out["reverse"] = obj
		}
	}
	// A zero keepAlive on the record means "not a tunnel client"; sending it
	// would ask the panel to validate a keepalive that was never set.
	switch v := out["keepAlive"].(type) {
	case nil, string:
		if v == nil || v == "" {
			delete(out, "keepAlive")
		}
	case float64:
		if v == 0 {
			delete(out, "keepAlive")
		}
	}
	return out
}

// CreateClient adds a client to the given inbounds. Fields left out are filled
// by the panel: the UUID or password the protocol needs, and the subId.
func (c *Client) CreateClient(client map[string]any, inboundIds []int) error {
	_, err := c.PostJSON("clients/add", map[string]any{"client": client, "inboundIds": inboundIds})
	return err
}

// UpdateClient rewrites one client after mutate has changed the fields it
// wants. The panel replaces every non-credential field with what it is sent,
// so the full record is read first and only mutate's changes differ.
func (c *Client) UpdateClient(email string, mutate func(client map[string]any)) error {
	d, err := c.GetClient(email)
	if err != nil {
		return err
	}
	client := d.AsClient()
	mutate(client)
	_, err = c.PostJSON("clients/update/"+url.PathEscape(email), client)
	return err
}

// SetClientEnabled uses the bulk endpoint, which flips only the flag.
func (c *Client) SetClientEnabled(email string, enable bool) error {
	path := "clients/bulkDisable"
	if enable {
		path = "clients/bulkEnable"
	}
	_, err := c.PostJSON(path, map[string]any{"emails": []string{email}})
	return err
}

func (c *Client) ResetClientTraffic(email string) error {
	_, err := c.Post("clients/resetTraffic/"+url.PathEscape(email), nil)
	return err
}

func (c *Client) DeleteClient(email string) error {
	_, err := c.Post("clients/del/"+url.PathEscape(email), nil)
	return err
}
