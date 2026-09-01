// Package auth is what the two front ends share about the caller: how a
// request presents a credential, and what that credential is allowed to do.
//
// It exists because there are now two answers to "may this request write",
// and two places that have to agree on them. The HTTP API can decide from the
// method — a GET reads and everything else does not — but the MCP endpoint is
// POST for every call it will ever serve, so it has to decide per tool. Both
// need the same word for the same thing, and neither is layered on the other:
// they are sibling clients of the same service.
package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Role is what a presented credential may do.
type Role string

const (
	// Full may read and write. It is also the role of a request to a server
	// with no token configured at all, which is the loopback default.
	Full Role = "full"

	// ReadOnly may read and nothing else. It exists so that an assistant, or
	// a dashboard, or anything else that should look without touching, can be
	// given a credential that cannot be talked into writing.
	ReadOnly Role = "read-only"
)

// CanWrite reports whether a role may change anything.
func (r Role) CanWrite() bool { return r != ReadOnly }

// Scopes names a role the way GET /v1/me reports it.
func (r Role) Scopes() []string {
	if r == ReadOnly {
		return []string{"read"}
	}
	return []string{"read", "write"}
}

// TokenFrom returns the credential a request presents.
//
// Two headers are accepted for one token. Authorization is the correct one and
// what the schema documents; X-Api-Key is here because a good deal of software
// that would otherwise be a fine client of this API can set an arbitrary
// header and cannot construct an Authorization one — several MCP clients among
// them. Refusing them on principle would not make anything safer, since the
// secret and what it unlocks are identical either way.
//
// Authorization wins when both are present, so that a client which sets one
// deliberately is not overruled by one a proxy added.
func TokenFrom(r *http.Request) string {
	const prefix = "Bearer "
	if got := r.Header.Get("Authorization"); strings.HasPrefix(got, prefix) {
		return got[len(prefix):]
	}
	return r.Header.Get("X-Api-Key")
}

// Check returns the role a presented token earns.
//
// An empty full token means no authentication is configured, and everything is
// allowed — the loopback default. Both tokens are compared every time, in
// constant time, so that neither the answer nor how long it took says which
// one nearly matched.
func Check(presented, full, readOnly string) (Role, bool) {
	if full == "" {
		return Full, true
	}
	isFull := equal(presented, full)
	isRead := readOnly != "" && equal(presented, readOnly)
	switch {
	case isFull:
		return Full, true
	case isRead:
		return ReadOnly, true
	}
	return "", false
}

// equal compares in constant time. subtle.ConstantTimeCompare returns early on
// a length mismatch, so the lengths are equalised first: a token's length is
// not a secret worth much, but leaking it costs nothing to avoid.
func equal(a, b string) bool {
	if len(a) != len(b) {
		// Still do the work, so that a wrong length is not measurably faster
		// than a wrong token.
		subtle.ConstantTimeCompare([]byte(b), []byte(b))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// roleKey is the context key. An unexported struct type cannot collide with a
// key set by anything else.
type roleKey struct{}

// WithRole records the caller's role on a request context.
func WithRole(ctx context.Context, r Role) context.Context {
	return context.WithValue(ctx, roleKey{}, r)
}

// RoleOf returns the role recorded on a context.
//
// It answers Full when nothing was recorded, which is the same thing an
// unauthenticated server answers: a handler reached without passing a token
// check is one on a server that has no token to check.
func RoleOf(ctx context.Context) Role {
	if r, ok := ctx.Value(roleKey{}).(Role); ok {
		return r
	}
	return Full
}
