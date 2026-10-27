package gitlab

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/platform/egress"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

// fail maps an error of the platform client to the *app.GitLabError of the ports:
//   - a context that ended is the caller's doing: no status, not a transport failure;
//   - a GitLab answer keeps its status (the 3xx and the non-API answers have none, and a redirect
//     is never followed with the token);
//   - a token that cannot be sent as a header is a rejected credential (401, no request);
//   - a failure before any answer (dial, TLS, timeout, truncated body) is a transport failure.
//
// token is the call's token, used only to strip it from a cause whose text would show it.
func fail(ctx context.Context, step string, err error, token string) *app.GitLabError {
	ge := &app.GitLabError{Step: step, Cause: strip(err, token)}
	var se *glclient.StatusError
	switch {
	case ctx.Err() != nil:
	case errors.As(err, &se):
		ge.Status = se.Status
	case errors.Is(err, glclient.ErrToken):
		ge.Status = 401
	case errors.Is(err, glclient.ErrRedirect), errors.Is(err, glclient.ErrNotAPI),
		errors.Is(err, glclient.ErrBaseURL), errors.Is(err, glclient.ErrTooLarge):
	default:
		ge.Transport = isTransport(err)
	}
	return ge
}

// strip returns err unchanged unless its text contains the token, in which case it returns an error
// of the same text with the token masked (the platform client scrubs already; this is the second
// line, and it costs a string search).
func strip(err error, token string) error {
	if err == nil || len(token) < 8 || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
}

// isTransport reports whether err is a failure before a response: a dial or DNS failure, a TLS
// failure, a timeout, a reset or truncated connection, or the dial guard's refusal.
func isTransport(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ETIMEDOUT) || egress.IsForbidden(err) {
		return true
	}
	var (
		ne      net.Error
		dns     *net.DNSError
		verify  *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		invalid x509.CertificateInvalidError
		record  tls.RecordHeaderError
		alert   tls.AlertError
	)
	return errors.As(err, &ne) || errors.As(err, &dns) || errors.As(err, &verify) || errors.As(err, &unknown) ||
		errors.As(err, &host) || errors.As(err, &invalid) || errors.As(err, &record) || errors.As(err, &alert)
}
