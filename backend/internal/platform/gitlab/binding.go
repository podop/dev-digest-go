package gitlab

import (
	"context"
	"fmt"
	"strings"
)

// apiSuffix is appended to a connection's instance URL to get its REST root.
const apiSuffix = "/api/v4"

// Binding is a stored GitLab connection together with its token, as the contexts that read
// GitLab on a repository's behalf need it. BaseURL is the instance URL (https://host[:port]
// [/prefix], no trailing slash, no /api/v4); Host is the connection's host key. The composition
// root builds it from the connections service; it is read on every call, so a rotated token
// applies to the next request. Its text forms never show the token.
type Binding struct {
	ID      string
	Name    string
	BaseURL string
	Host    string
	Token   string
}

// Conn returns the REST connection for b: BaseURL + /api/v4 and the token.
func (b Binding) Conn() Conn {
	return Conn{BaseURL: strings.TrimRight(b.BaseURL, "/") + apiSuffix, Token: b.Token}
}

// String shows the binding without its token, so a %v or %+v of a Binding cannot leak it.
func (b Binding) String() string {
	return fmt.Sprintf("gitlab.Binding{ID: %q, Name: %q, BaseURL: %q, Host: %q, Token: %s}",
		b.ID, b.Name, b.BaseURL, b.Host, redactedToken(b.Token))
}

// GoString is the %#v form; it hides the token as String does.
func (b Binding) GoString() string { return b.String() }

func redactedToken(tok string) string {
	if tok == "" {
		return `""`
	}
	return `"` + mask + `"`
}

// ResolveFunc finds the connection whose host key equals hostKey; ok is false when there is none
// (spec A FR48). An error is a failure to read the connections, not an absent one.
type ResolveFunc func(ctx context.Context, hostKey string) (b Binding, ok bool, err error)

// ListFunc lists every connection. Callers match a URL against them.
type ListFunc func(ctx context.Context) ([]Binding, error)
