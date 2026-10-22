package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	rdomain "dev-digest-go/backend/internal/reviewer/domain"
)

// nodeCases are reference values computed by Node's own implementation (dev-digest d1c9093) with
// testdata/node_gen.mts. ONBOARDING_NODE_CASES points the tests at a bigger corpus generated
// with the same script (--n 4000); the committed file is the small default.
type nodeCases struct {
	SystemPrompt string
	Redact       []struct{ In, Out string }
	Command      []struct {
		In  string
		Out bool
	}
	Excerpts []struct {
		In      json.RawMessage
		Readme  *string
		Compose *string
	}
	Scripts []struct {
		In  string
		Out *string
	}
	EnvNames []struct {
		In  string
		Out []string
	}
	Todo []struct {
		Path, In string
		Out      []todoWire
	}
	Collect []struct {
		PerFile [][]todoWire
		Out     []todoWire
	}
	Untested []struct {
		Ranked, Indexed, Out []string
	}
	TestPath []struct {
		In  string
		Out bool
	}
	Prompt        []promptCaseWire
	NormalizePath []struct{ In, Out string }
	TourFiles     []string
	Tour          []tourCaseWire
	Stale         []struct {
		StoredSha           string
		StoredPromptVersion int
		CurrentSha          *string
		Out                 struct {
			Stale  bool
			Reason string
		}
	}
}

type todoWire struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

var (
	casesOnce sync.Once
	cases     *nodeCases
)

func loadCases(t testing.TB) *nodeCases {
	t.Helper()
	casesOnce.Do(func() {
		path := os.Getenv("ONBOARDING_NODE_CASES")
		if path == "" {
			path = "testdata/node_cases.json"
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var c nodeCases
		if err := json.Unmarshal(b, &c); err != nil {
			t.Errorf("decode %s: %v", path, err)
			return
		}
		cases = &c
	})
	if cases == nil {
		t.Fatal("Node reference cases not loaded (testdata/node_cases.json)")
	}
	return cases
}

// expand turns the generator's string specs into text: {"rep":unit,"n":N}, {"lines":[n,w]},
// {"cat":[...]}, {"head":N,"of":spec} (BMP text only); other values pass through.
func expand(t testing.TB, v any) any {
	t.Helper()
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = expand(t, e)
		}
		return out
	case map[string]any:
		switch {
		case x["rep"] != nil:
			return strings.Repeat(x["rep"].(string), int(x["n"].(float64)))
		case x["lines"] != nil:
			l := x["lines"].([]any)
			n, w := int(l[0].(float64)), int(l[1].(float64))
			var sb strings.Builder
			for i := 0; i < n; i++ {
				if i > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString("src/dir" + itoa(i%17) + "/file" + itoa(i) + ".ts" + strings.Repeat(" ", w))
			}
			return sb.String()
		case x["cat"] != nil:
			var sb strings.Builder
			for _, e := range x["cat"].([]any) {
				sb.WriteString(expand(t, e).(string))
			}
			return sb.String()
		case x["head"] != nil:
			return rdomain.PrefixUnits(expand(t, x["of"]).(string), int(x["head"].(float64)))
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = expand(t, e)
		}
		return out
	}
	return v
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func expandText(t testing.TB, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode spec %s: %v", raw, err)
	}
	s, ok := expand(t, v).(string)
	if !ok {
		t.Fatalf("spec %s is not text", raw)
	}
	return s
}

func shaOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// shorten keeps failure messages readable.
func shorten(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
