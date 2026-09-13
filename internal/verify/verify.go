// Package verify checks that an applied routing change actually takes effect.
// The panel's routeTest only reflects the running core, so a rule can only be
// verified after it is written — which is also the only place a rule shadowed
// by an earlier one shows up at all.
package verify

import (
	"fmt"
	"net"
	"strings"

	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/state"
)

// entrySampleSize bounds the category page fetched to find a testable member;
// the first entries are enough and a whole category can be enormous.
const entrySampleSize = 25

// The panel rejects an empty geodata file name, so a geosite:/geoip: token has
// to name the database its prefix implies.
const (
	defaultSiteFile = "geosite.dat"
	defaultIPFile   = "geoip.dat"
)

type Verifier struct {
	panel *panel.Client
}

func New(p *panel.Client) *Verifier { return &Verifier{panel: p} }

type Result struct {
	// Source is the rule value the check came from, Target the concrete
	// destination it resolved to — they differ for a category token.
	Source string
	Target string
	Expect string
	Actual string
	OK     bool
	// User is the identity the probe ran as, empty when the rule is not scoped.
	User string
	// Skipped explains why no assertion could be made; OK is meaningless then.
	Skipped string
	// Index is the position of the rule under test.
	Index int
}

func (r Result) Verified() bool { return r.Skipped == "" }

func (v *Verifier) Run(checks []state.Check) []Result {
	out := make([]Result, 0, len(checks))
	for _, ch := range checks {
		out = append(out, v.one(ch))
	}
	return out
}

func (v *Verifier) one(ch state.Check) Result {
	res := Result{Source: ch.Value, Expect: ch.Expect, Index: ch.Index, User: ch.User}

	target, reason := v.resolve(ch)
	if target == "" {
		res.Skipped = reason
		return res
	}
	res.Target = target

	req := panel.RouteTestRequest{Port: 443, Network: "tcp", Email: ch.User}
	if ch.Kind == "ip" {
		req.IP = target
	} else {
		req.Domain = target
	}
	got, err := v.panel.RouteTest(req)
	if err != nil {
		res.Skipped = "测试调用失败: " + err.Error()
		return res
	}
	if !got.Matched {
		res.Actual = "(未匹配任何规则)"
		return res
	}
	res.Actual = got.OutboundTag
	res.OK = got.OutboundTag == ch.Expect
	return res
}

// resolve turns a rule value into one concrete destination to test, or returns
// the reason it cannot be turned into one.
func (v *Verifier) resolve(ch state.Check) (string, string) {
	value := strings.TrimSpace(ch.Value)
	lower := strings.ToLower(value)

	switch {
	case strings.HasPrefix(lower, "domain:"), strings.HasPrefix(lower, "full:"):
		return value[strings.Index(value, ":")+1:], ""
	case strings.HasPrefix(lower, "keyword:"):
		return "", "关键字匹配无法取样验证"
	case strings.HasPrefix(lower, "regexp:"):
		return "", "正则匹配无法取样验证"
	case strings.HasPrefix(lower, "geosite:"):
		return v.sampleCategory(defaultSiteFile, value[len("geosite:"):], false)
	case strings.HasPrefix(lower, "geoip:"):
		return v.sampleCategory(defaultIPFile, value[len("geoip:"):], true)
	case strings.HasPrefix(lower, "ext:"):
		file, code, ok := splitExt(value)
		if !ok {
			return "", "ext: 写法无法解析"
		}
		return v.sampleCategory(file, code, ch.Kind == "ip")
	}

	if ch.Kind == "ip" {
		if ip := net.ParseIP(value); ip != nil {
			return value, ""
		}
		if _, _, err := net.ParseCIDR(value); err == nil {
			return "", "网段无法直接测试，需要具体 IP"
		}
		return "", "认不出这个 IP 写法"
	}
	return value, ""
}

// sampleCategory pulls one testable member out of a geo database category.
func (v *Verifier) sampleCategory(file, code string, isIP bool) (string, string) {
	page, err := v.panel.GeodataEntries(file, code, "", 0, entrySampleSize)
	if err != nil {
		return "", fmt.Sprintf("读取分类 %s 失败: %v", code, err)
	}
	if page == nil || len(page.Items) == 0 {
		return "", fmt.Sprintf("分类 %s 是空的或不存在", code)
	}
	for _, e := range page.Items {
		if target := testableEntry(e, isIP); target != "" {
			return target, ""
		}
	}
	return "", fmt.Sprintf("分类 %s 里没有可直接取样的条目", code)
}

// testableEntry picks a member routeTest can be called with: a suffix or exact
// domain, or a concrete address. Keyword and regexp members describe a pattern,
// not a destination, so no probe can be constructed from them.
func testableEntry(entry panel.GeoEntry, isIP bool) string {
	switch strings.ToLower(entry.Kind) {
	case "keyword", "regexp":
		return ""
	}
	e := strings.TrimSpace(entry.Value)
	if e == "" {
		return ""
	}
	if !isIP {
		return e
	}
	if ip := net.ParseIP(e); ip != nil {
		return e
	}
	// A CIDR's own network address is inside the range, so it is a valid probe.
	if ip, _, err := net.ParseCIDR(e); err == nil {
		return ip.String()
	}
	return ""
}

func splitExt(value string) (file, code string, ok bool) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 {
		return "", "", false
	}
	return parts[1], parts[2], true
}
