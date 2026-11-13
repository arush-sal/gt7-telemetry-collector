package transport

import "strings"

const (
	// AuthHeader is the HTTP header carrying ingest authentication.
	AuthHeader = "Authorization"
	// AuthMetadataKey is the gRPC metadata key carrying ingest authentication.
	AuthMetadataKey = "authorization"
	// BearerPrefix precedes an ingest bearer token on the wire.
	BearerPrefix = "Bearer "
)

// Option configures a publisher created by New.
type Option func(*options)

type options struct{ token string }

// WithToken supplies an optional bearer token for HTTP(S) and gRPC ingest.
// Network tokens must match RFC 6750 b64token syntax with no whitespace.
// An empty token sends no authentication. Unix sockets ignore the token and
// rely on socket file permissions for access control.
func WithToken(token string) Option {
	return func(o *options) { o.token = token }
}

// redactedError preserves error identity while hiding a token in diagnostics.
type redactedError struct {
	err   error
	token string
}

func (e redactedError) Error() string {
	return strings.ReplaceAll(e.err.Error(), e.token, "[REDACTED]")
}
func (e redactedError) Unwrap() error { return e.err }

// validToken implements b64token: one or more allowed bytes, then optional '=' padding.
func validToken(token string) bool {
	base := strings.TrimRight(token, "=")
	if base == "" {
		return false
	}
	for i := 0; i < len(base); i++ {
		b := base[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~+/", rune(b))) {
			return false
		}
	}
	return true
}
