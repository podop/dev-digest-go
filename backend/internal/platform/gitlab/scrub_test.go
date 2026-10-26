package gitlab

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	const tok = "glpat-AbCdEfGhIjKlMnOpQrSt"
	quoted := strconv.Quote("x" + tok + `"`)
	jsonTok, _ := json.Marshal(`a"` + tok)
	tests := []struct {
		name, in string
	}{
		{"plain", "bad token " + tok + " rejected"},
		{"quoted", "header " + quoted},
		{"json", string(jsonTok)},
		{"url escaped", "?private_token=" + url.QueryEscape(tok)},
		{"fragment", "token ending in " + tok[10:] + " is wrong"},
		{"prefix fragment", "starts with " + tok[:14]},
		{"other token shape", "got glpat-ZZZZZZZZZZZZZZZZZZZZ and gldt-0123456789abcdef"},
		{"runner registration token", "got glrtr-ABCDEFGHIJKLMNOPQRSTUV end"},
		{"routable token", "got glpat-ZZZZZZZZZZZZZZZZZZZZ.01.1a2b3c4d5 end"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scrub(tc.in, tok)
			for i := 0; i+minFragment <= len(tok); i++ {
				if strings.Contains(got, tok[i:i+minFragment]) {
					t.Errorf("scrub(%q) = %q, still holds %q", tc.in, got, tok[i:i+minFragment])
				}
			}
			// Token-shaped runs and routable suffixes must go with the rest.
			for _, left := range []string{"glpat-Z", "gldt-0", "glrtr-", "ABCDEFGH", ".01.", "1a2b3c4d5"} {
				if strings.Contains(got, left) {
					t.Errorf("scrub(%q) = %q, still holds %q", tc.in, got, left)
				}
			}
		})
	}
}

// NFR1: git sends the token as HTTP Basic "oauth2:<token>"; an error body that echoes that header
// must not reach a log line.
func TestScrub_MasksTheBasicCredential(t *testing.T) {
	const tok = "glpat-AbCdEfGhIjKlMnOpQrSt"
	basic := base64.StdEncoding.EncodeToString([]byte("oauth2:" + tok))
	in := "403 Forbidden (credential " + basic + ")"
	want := "403 Forbidden (credential " + mask + ")"
	if got := scrub(in, tok); got != want {
		t.Errorf("scrub(%q) = %q, want %q", in, got, want)
	}
}

func TestScrub_LeavesPlainText(t *testing.T) {
	const in = "401 Unauthorized: the project was not found"
	if got := scrub(in, "glpat-AbCdEfGhIjKlMnOpQrSt"); got != in {
		t.Errorf("scrub(%q) = %q, want unchanged", in, got)
	}
	if got := scrub(in, ""); got != in {
		t.Errorf("scrub(%q, \"\") = %q, want unchanged", in, got)
	}
}

func FuzzScrub(f *testing.F) {
	f.Add("glpat-AbCdEfGhIjKlMnOpQrSt", "error: glpat-AbCdEfGhIjKlMnOpQrSt")
	f.Add("glpat-0123456789abcdefghij", `"glpat-0123456789abcdefghij"`)
	f.Fuzz(func(t *testing.T, token, text string) {
		if len(token) < minFragment {
			return
		}
		got := scrub(text+token+text, token)
		if strings.Contains(got, token) {
			t.Errorf("scrub left the token in %q", got)
		}
	})
}
