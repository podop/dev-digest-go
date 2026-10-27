package gitlab

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// An unparsable redirect Location is quoted by net/http before CheckRedirect runs; the error must
// be ErrRedirect and must not carry the Location (it may be a signed URL).
func TestTransportError_UnparsableLocationIsARedirectWithoutTheTarget(t *testing.T) {
	const target = "https://storage.example/obj?X-Amz-Signature=secret\x7f"
	err := &url.Error{Op: "Get", URL: "https://gitlab.example/api/v4/x",
		Err: fmt.Errorf("failed to parse Location header %q: invalid control character in URL", target)}
	got := transportError("read", err, "glpat-token")
	if !errors.Is(got, ErrRedirect) {
		t.Errorf("transportError() = %v, want ErrRedirect", got)
	}
	if strings.Contains(got.Error(), "X-Amz-Signature") || strings.Contains(got.Error(), "storage.example") {
		t.Errorf("transportError() = %q, must not echo the Location", got)
	}
}
