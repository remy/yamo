package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenFrom(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"bearer", map[string]string{"Authorization": "Bearer abc"}, "abc"},
		{"api key", map[string]string{"X-Api-Key": "abc"}, "abc"},
		{"authorization wins", map[string]string{"Authorization": "Bearer abc", "X-Api-Key": "xyz"}, "abc"},
		{"other scheme falls through", map[string]string{"Authorization": "Basic abc", "X-Api-Key": "xyz"}, "xyz"},
		{"nothing", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := TokenFrom(r); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name      string
		presented string
		full      string
		readOnly  string
		want      Role
		ok        bool
	}{
		{"no token configured", "", "", "", Full, true},
		{"no token configured, anything presented", "junk", "", "", Full, true},
		{"the full token", "f", "f", "r", Full, true},
		{"the read-only token", "r", "f", "r", ReadOnly, true},
		{"neither", "x", "f", "r", "", false},
		{"nothing presented", "", "f", "r", "", false},
		{"no read-only token configured", "r", "f", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Check(c.presented, c.full, c.readOnly)
			if ok != c.ok || got != c.want {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestRoleOfDefaultsToFull(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := RoleOf(r.Context()); got != Full {
		t.Errorf("an unmarked context is %q; a handler reached without a token check is on a "+
			"server with no token to check", got)
	}
	ctx := WithRole(r.Context(), ReadOnly)
	if got := RoleOf(ctx); got != ReadOnly {
		t.Errorf("got %q, want %q", got, ReadOnly)
	}
	if ReadOnly.CanWrite() {
		t.Error("the read-only role may write")
	}
	if !Full.CanWrite() {
		t.Error("the full role may not write")
	}
}
