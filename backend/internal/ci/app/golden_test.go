package app_test

// Parity of the ci use cases with the Node services (commit d1c9093): the scenarios of the golden
// testdata/goldens/ci.json (`export`, `ingest`) run against the Go services over fake ports. The
// inputs ("world", steps) are read from the corpus testdata/corpus/ci/flows.json, which holds the
// defaults and the placeholders; the expected outcomes come from the golden. Where Go differs on
// purpose (runner files instead of index.js, the workflow without Node, the PR body with a runner
// paragraph, the manifest text) the comparison says so in its own helper.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	goldenPath = "../../parity/testdata/goldens/ci.json"
	corpusPath = "../../parity/testdata/corpus/ci/flows.json"
)

type obj = map[string]any

func readJSONFile(t testing.TB, path string) obj {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v obj
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return v
}

// scenarios returns the corpus and golden scenarios of one flow, paired by position and name.
func scenarios(t testing.TB, flow string) (spec obj, corpus, golden []obj) {
	t.Helper()
	flows := readJSONFile(t, corpusPath)
	gold := readJSONFile(t, goldenPath)
	spec = flows[flow].(obj)
	for _, s := range spec["scenarios"].([]any) {
		corpus = append(corpus, s.(obj))
	}
	for _, s := range gold[flow].([]any) {
		golden = append(golden, s.(obj))
	}
	if len(corpus) != len(golden) {
		t.Fatalf("%s: %d corpus scenarios, %d golden scenarios", flow, len(corpus), len(golden))
	}
	for i := range corpus {
		if corpus[i]["name"] != golden[i]["name"] {
			t.Fatalf("%s[%d]: corpus %q, golden %q", flow, i, corpus[i]["name"], golden[i]["name"])
		}
	}
	return spec, corpus, golden
}

// mergeObj is the generator's deep merge: objects merge, everything else is replaced.
func mergeObj(base, over obj) obj {
	out := obj{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		bm, bok := out[k].(obj)
		om, ook := v.(obj)
		if bok && ook {
			out[k] = mergeObj(bm, om)
		} else {
			out[k] = v
		}
	}
	return out
}

// mergeAbsent is mergeObj where the value "$absent" deletes the key.
func mergeAbsent(base, over obj) obj {
	out := mergeObj(base, over)
	for k, v := range over {
		if v == "$absent" {
			delete(out, k)
		}
	}
	return out
}

var uEscape = regexp.MustCompile(`\\u\{([0-9a-fA-F]+)\}`)

// expand resolves the corpus placeholders of a string value: \u{HEX}, {"$repeat": [s, n]} and
// {"$concat": [..]}.
func expand(t testing.TB, v any) any {
	t.Helper()
	switch x := v.(type) {
	case string:
		return uEscape.ReplaceAllStringFunc(x, func(m string) string {
			n, err := strconv.ParseInt(uEscape.FindStringSubmatch(m)[1], 16, 32)
			if err != nil {
				t.Fatalf("bad escape %s", m)
			}
			return string(rune(n))
		})
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = expand(t, e)
		}
		return out
	case obj:
		if r, ok := x["$repeat"].([]any); ok {
			return strings.Repeat(expand(t, r[0]).(string), int(r[1].(float64)))
		}
		if c, ok := x["$concat"].([]any); ok {
			var sb strings.Builder
			for _, p := range c {
				sb.WriteString(expand(t, p).(string))
			}
			return sb.String()
		}
		out := obj{}
		for k, e := range x {
			out[k] = expand(t, e)
		}
		return out
	}
	return v
}

// Small accessors over decoded JSON; a missing key gives the zero value.
func asObj(v any) obj {
	m, _ := v.(obj)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asStr(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func asInt64(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func optInt(v any) *int {
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	n := int(f)
	return &n
}

func optStr(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}
