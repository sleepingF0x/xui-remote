// Package detect classifies free-form text the user sends so modules can offer
// the actions that fit it. Classification never acts — it only labels.
package detect

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

type Kind string

const (
	KindDomain Kind = "domain"
	KindIP     Kind = "ip"
	KindCIDR   Kind = "cidr"
	KindEmail  Kind = "email"
	KindURL    Kind = "url"
	Unknown    Kind = ""
)

type Result struct {
	Kind Kind
	// Value is the normalized form: a URL reduces to its hostname, a domain
	// loses a leading "www." because suffix matching already covers it.
	Value string
	// Original is what the user actually sent, for echoing back.
	Original string
}

var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func Classify(input string) Result {
	s := strings.TrimSpace(input)
	if s == "" {
		return Result{Kind: Unknown, Original: input}
	}

	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return Result{Kind: KindURL, Value: stripWWW(u.Hostname()), Original: s}
		}
	}
	if _, ipnet, err := net.ParseCIDR(s); err == nil {
		return Result{Kind: KindCIDR, Value: ipnet.String(), Original: s}
	}
	if ip := net.ParseIP(s); ip != nil {
		return Result{Kind: KindIP, Value: ip.String(), Original: s}
	}
	// An email that is also a valid hostname-looking string is treated as an
	// email: x-ui client identifiers are emails, and domains never contain "@".
	if emailRe.MatchString(s) {
		return Result{Kind: KindEmail, Value: s, Original: s}
	}
	// A bare label like "admin" is a valid x-ui client name but not a domain.
	if strings.Contains(s, ".") && domainRe.MatchString(s) {
		return Result{Kind: KindDomain, Value: stripWWW(s), Original: s}
	}
	return Result{Kind: Unknown, Value: s, Original: s}
}

// IsHostLike reports whether the input names a network destination, which is
// what a route test needs regardless of how it was written.
func (r Result) IsHostLike() bool {
	return r.Kind == KindDomain || r.Kind == KindIP || r.Kind == KindURL
}

func stripWWW(host string) string {
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}
