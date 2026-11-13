package browserstream

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// tokenPrefix marks the Sec-WebSocket-Protocol entry carrying the access token (ADR 0003).
const tokenPrefix = "gt7.token."

// tokenChars is the token alphabet; all are RFC 6455 subprotocol token characters.
const tokenChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._~-"

// hostChars is the origin host alphabet outside IPv6 brackets.
const hostChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-"

// maxSubprotocols bounds the number of offered Sec-WebSocket-Protocol entries.
const maxSubprotocols = 8

// Subprotocol parse errors. They are fixed values and never contain header content.
var (
	errNoProtocol    = errors.New("gt7.inputs.v1 not offered")
	errProtocolCount = errors.New("gt7.inputs.v1 must be offered exactly once")
	errEmptyEntry    = errors.New("empty subprotocol entry")
	errTooMany       = errors.New("too many subprotocol entries")
	errTokenCount    = errors.New("more than one token entry")
)

// NormalizeOrigin validates an origin (scheme://host[:port], http or https) and returns its
// canonical form: lowercase scheme and host, default port stripped. Allowlist entries and request
// Origin headers both go through it and are then compared with ==.
func NormalizeOrigin(raw string) (string, error) {
	if raw == "" || raw == "null" || strings.ContainsAny(raw, "*?#@%\\ ") {
		return "", errors.New("origin must be scheme://host[:port] without wildcard, path, query, fragment, userinfo or escapes")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("origin is not a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("origin scheme must be http or https")
	}
	if u.Opaque != "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("origin must not have a path, query, fragment or userinfo")
	}
	hostport := u.Host
	host, port := hostport, ""
	if i := strings.LastIndexByte(hostport, ':'); i >= 0 && !strings.HasSuffix(hostport, "]") {
		host, port = hostport[:i], hostport[i+1:]
		if !validPort(port) {
			return "", errors.New("origin port must be 1..65535 without leading zeros")
		}
	}
	if strings.HasPrefix(host, "[") {
		a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
		if err != nil || !strings.HasSuffix(host, "]") || !a.Is6() || a.Zone() != "" {
			return "", errors.New("origin IPv6 host is invalid")
		}
	} else if host == "" || strings.Trim(host, hostChars) != "" {
		return "", errors.New("origin host must be a DNS name, IPv4 or bracketed IPv6 literal")
	}
	host = strings.ToLower(host)
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

// validPort reports whether p is a canonical decimal port in 1..65535.
func validPort(p string) bool {
	n, err := strconv.ParseUint(p, 10, 16)
	return err == nil && n > 0 && p[0] != '0'
}

// ValidateToken checks the GT7_WS_TOKEN syntax: 32..256 characters of [A-Za-z0-9._~-]. The error
// never includes the token.
func ValidateToken(tok string) error {
	if len(tok) < 32 || len(tok) > 256 {
		return fmt.Errorf("token must be 32..256 characters, got %d", len(tok))
	}
	for i := 0; i < len(tok); i++ {
		if !strings.ContainsRune(tokenChars, rune(tok[i])) {
			return fmt.Errorf("token contains an invalid character at position %d (allowed: A-Z a-z 0-9 . _ ~ -)", i)
		}
	}
	return nil
}

// ParseListen validates a --ws-listen value. The host must be an IP literal, empty, or
// "localhost" (rewritten to 127.0.0.1, no DNS lookup); the port must be 1..65535. loopback is
// true for 127.0.0.0/8, ::1 and IPv4-mapped loopback without a zone.
func ParseListen(addr string) (bindAddr string, loopback bool, err error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", false, fmt.Errorf("listen address %q: %w", addr, err)
	}
	if !validPort(port) {
		return "", false, fmt.Errorf("listen address %q: port must be 1..65535", addr)
	}
	switch {
	case host == "":
		return addr, false, nil
	case strings.EqualFold(host, "localhost"):
		return net.JoinHostPort("127.0.0.1", port), true, nil
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return "", false, fmt.Errorf("listen address %q: host must be an IP literal or localhost", addr)
	}
	return net.JoinHostPort(a.String(), port), a.Zone() == "" && a.Unmap().IsLoopback(), nil
}

// loopbackHost reports whether a Host header names the loopback interface: a loopback IP literal
// or "localhost" (any case, no trailing dot), with or without a port.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Zone() == "" && a.Unmap().IsLoopback()
}

// parseSubprotocols applies the ADR 0003 Sec-WebSocket-Protocol rules over all header lines and
// returns the offered token, if any. Errors are the fixed sentinels above.
func parseSubprotocols(h http.Header) (token string, hasToken bool, err error) {
	n, protos := 0, 0
	for _, v := range h.Values("Sec-WebSocket-Protocol") {
		for e := range strings.SplitSeq(v, ",") {
			e = strings.TrimSpace(e)
			n++
			switch {
			case e == "":
				return "", false, errEmptyEntry
			case n > maxSubprotocols:
				return "", false, errTooMany
			case strings.EqualFold(e, Protocol):
				// Case variants count too: the library would otherwise negotiate them.
				if e != Protocol {
					return "", false, errProtocolCount
				}
				protos++
			case strings.HasPrefix(e, tokenPrefix):
				if hasToken {
					return "", false, errTokenCount
				}
				token, hasToken = e[len(tokenPrefix):], true
			}
		}
	}
	switch {
	case protos == 0:
		return "", false, errNoProtocol
	case protos > 1:
		return "", false, errProtocolCount
	}
	return token, hasToken, nil
}

// originPatterns turns normalized origins into coder/websocket OriginPatterns (path.Match syntax,
// matched against "scheme://host" because the pattern contains "://"). Metacharacters are escaped,
// and origins without a port also get the explicit default-port form, so the library never refuses
// an origin the server's own exact check accepted.
func originPatterns(origins []string) []string {
	esc := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`)
	var out []string
	for _, o := range origins {
		out = append(out, esc.Replace(o))
		scheme, host, _ := strings.Cut(o, "://")
		if strings.HasSuffix(host, "]") || !strings.Contains(host, ":") {
			def := map[string]string{"http": ":80", "https": ":443"}[scheme]
			out = append(out, esc.Replace(o+def))
		}
	}
	return out
}
