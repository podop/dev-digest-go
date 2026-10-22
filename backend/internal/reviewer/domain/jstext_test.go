package domain_test

// Reference values in testdata/jstext_cases.json come from Node (testdata/jstext_gen.mjs): String.trim /
// slice, localeCompare('en'), default Array.sort, decodeURIComponent, Number toString / toFixed and
// JSON.stringify over seeded random inputs full of surrogate pairs, NBSP, BOM, U+0085, U+2028/9.

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviewer/domain"
)

type jsTextCases struct {
	Cut []struct {
		Text      string `json:"text"`
		Max       int    `json:"max"`
		Want      string `json:"want"`
		Truncated bool   `json:"truncated"`
	} `json:"cut"`
	Order []struct {
		A, B  string
		Units int `json:"units"`
		En    int `json:"en"`
	} `json:"order"`
	Sorts []struct {
		Items   []string `json:"items"`
		Units   []string `json:"units"`
		English []string `json:"english"`
	} `json:"sorts"`
	Decode []struct {
		In   string `json:"in"`
		OK   bool   `json:"ok"`
		Want string `json:"want"`
		Err  string `json:"err"`
	} `json:"decode"`
	Numbers []struct {
		Value  float64           `json:"value"`
		String string            `json:"string"`
		JSON   string            `json:"json"`
		Fixed  map[string]string `json:"fixed"`
	} `json:"numbers"`
	Canonical []struct {
		Input string `json:"input"`
		Want  string `json:"want"`
	} `json:"canonical"`
}

func loadJSText(t *testing.T) jsTextCases {
	t.Helper()
	raw, err := os.ReadFile("testdata/jstext_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c jsTextCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCut_MatchesNode(t *testing.T) {
	cases := loadJSText(t).Cut
	if len(cases) < 500 {
		t.Fatalf("corpus has %d cut cases, want >= 500", len(cases))
	}
	for i, tc := range cases {
		got, truncated := domain.CutTruncated(tc.Text, tc.Max)
		if got != tc.Want || truncated != tc.Truncated {
			t.Errorf("case %d: CutTruncated(%q, %d) = (%q, %v), want (%q, %v)", i, tc.Text, tc.Max, got, truncated, tc.Want, tc.Truncated)
		}
		if g := domain.Cut(tc.Text, tc.Max); g != tc.Want {
			t.Errorf("case %d: Cut(%q, %d) = %q, want %q", i, tc.Text, tc.Max, g, tc.Want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("case %d: Cut(%q, %d) = %q is not valid UTF-8", i, tc.Text, tc.Max, got)
		}
	}
}

func TestCut_Rules(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		max      int
		want     string
	}{
		{"trims JS whitespace incl. BOM and NBSP", "\ufeff\u00a0 abc \u2028", 10, "abc"},
		{"keeps U+0085, which JS does not trim", "\u0085abc", 10, "\u0085abc"},
		{"exactly max units is kept", "abcde", 5, "abcde"},
		{"one over max is cut to max-1 plus ellipsis", "abcdef", 5, "abcd…"},
		{"pair straddling the cut becomes U+FFFD", "ab😀cd", 4, "ab�…"},
		{"pair before the cut stays whole", "ab😀cd", 5, "ab😀…"},
		{"max 1 leaves only the ellipsis", "abc", 1, "…"},
		{"max 0 slices from the end like JS slice(0,-1)", "abc", 0, "ab…"},
		{"empty", "  ", 5, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.Cut(tc.in, tc.max); got != tc.want {
				t.Errorf("Cut(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
	// An emoji pair counts two units: 4 emoji = 8 units, cut to 6 units + the ellipsis.
	if got := domain.Len16(domain.Cut("😀😀😀😀", 7)); got != 7 {
		t.Errorf("Len16(Cut(4 emoji, 7)) = %d, want 7 (6 units of emoji + the ellipsis)", got)
	}
}

func TestCompareUnits_MatchesNode(t *testing.T) {
	for i, tc := range loadJSText(t).Order {
		if got := domain.CompareUnits(tc.A, tc.B); got != tc.Units {
			t.Errorf("case %d: CompareUnits(%q, %q) = %d, want %d", i, tc.A, tc.B, got, tc.Units)
		}
		if got := domain.LessUnits(tc.A, tc.B); got != (tc.Units < 0) {
			t.Errorf("case %d: LessUnits(%q, %q) = %v, want %v", i, tc.A, tc.B, got, tc.Units < 0)
		}
		if got := domain.EnglishComparer()(tc.A, tc.B); got != tc.En {
			t.Errorf("case %d: English compare(%q, %q) = %d, want %d", i, tc.A, tc.B, got, tc.En)
		}
	}
}

func TestCompareUnits_SupplementarySortsBeforeBMPHigh(t *testing.T) {
	// UTF-8 byte order puts U+E000 before U+10000; UTF-16 unit order (JS `<`) the opposite.
	if !domain.LessUnits("\U00010000", "") {
		t.Error(`LessUnits("U+10000", "U+E000") = false, want true`)
	}
	if domain.LessUnits("", "\U00010000") {
		t.Error(`LessUnits("U+E000", "U+10000") = true, want false`)
	}
}

func TestSorts_MatchNode(t *testing.T) {
	for i, tc := range loadJSText(t).Sorts {
		units := slices.Clone(tc.Items)
		slices.SortStableFunc(units, domain.CompareUnits)
		if diff := cmp.Diff(tc.Units, units); diff != "" {
			t.Errorf("case %d: code-unit sort of %q mismatch (-want +got):\n%s", i, tc.Items, diff)
		}
		english := slices.Clone(tc.Items)
		domain.SortEnglish(english)
		if diff := cmp.Diff(tc.English, english); diff != "" {
			t.Errorf("case %d: SortEnglish(%q) mismatch (-want +got):\n%s", i, tc.Items, diff)
		}
		type row struct{ k, tag string }
		rows := make([]row, len(tc.Items))
		for j, k := range tc.Items {
			rows[j] = row{k, "x"}
		}
		domain.SortEnglishBy(rows, func(r row) string { return r.k })
		got := make([]string, len(rows))
		for j, r := range rows {
			got[j] = r.k
		}
		if diff := cmp.Diff(tc.English, got); diff != "" {
			t.Errorf("case %d: SortEnglishBy(%q) mismatch (-want +got):\n%s", i, tc.Items, diff)
		}
	}
}

func TestSortEnglish_IsStableAndCaseInsensitiveFirst(t *testing.T) {
	got := []string{"b", "B", "a", "A", "a"}
	domain.SortEnglish(got)
	if diff := cmp.Diff([]string{"a", "a", "A", "b", "B"}, got); diff != "" {
		t.Errorf("SortEnglish mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeURIComponent_MatchesNode(t *testing.T) {
	cases := loadJSText(t).Decode
	var okN, errN int
	for i, tc := range cases {
		got, err := domain.DecodeURIComponent(tc.In)
		if tc.OK {
			okN++
			if err != nil || got != tc.Want {
				t.Errorf("case %d: DecodeURIComponent(%q) = (%q, %v), want (%q, nil)", i, tc.In, got, err, tc.Want)
			}
			continue
		}
		errN++
		if !errors.Is(err, domain.ErrURIMalformed) || err.Error() != tc.Err {
			t.Errorf("case %d: DecodeURIComponent(%q) = (%q, %v), want error %q", i, tc.In, got, err, tc.Err)
		}
	}
	if okN < 100 || errN < 100 {
		t.Errorf("corpus balance: %d ok, %d failing cases, want >= 100 of each", okN, errN)
	}
}

func TestDecodeURIComponent_Rules(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"docs%2Fa%20b.md", "docs/a b.md"},
		{"%e4%bd%a0", "你"},
		{"%F0%9F%98%80", "😀"},
		{"plain é 😀", "plain é 😀"},
	} {
		if got, err := domain.DecodeURIComponent(tc.in); err != nil || got != tc.want {
			t.Errorf("DecodeURIComponent(%q) = (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"%", "%4", "%zz", "%C0%80", "%ED%A0%80", "%F4%90%80%80", "%80", "%C3", "%C3%28"} {
		if _, err := domain.DecodeURIComponent(in); !errors.Is(err, domain.ErrURIMalformed) {
			t.Errorf("DecodeURIComponent(%q) error = %v, want URI malformed", in, err)
		}
	}
}

func TestJSNumberAndToFixed_MatchNode(t *testing.T) {
	cases := loadJSText(t).Numbers
	if len(cases) < 600 {
		t.Fatalf("corpus has %d number cases", len(cases))
	}
	for i, tc := range cases {
		if got := domain.JSNumber(tc.Value); got != tc.String {
			t.Errorf("case %d: JSNumber(%v) = %q, want %q", i, tc.Value, got, tc.String)
		}
		for digits, want := range tc.Fixed {
			n := int(digits[0] - '0')
			if got := domain.ToFixed(tc.Value, n); got != want {
				t.Errorf("case %d: ToFixed(%v, %d) = %q, want %q", i, tc.Value, n, got, want)
			}
		}
	}
}

func TestToFixed_EdgeCases(t *testing.T) {
	for _, tc := range []struct {
		v      float64
		digits int
		want   string
	}{
		{0.125, 2, "0.13"}, // Go's strconv gives 0.12 (ties to even); JS rounds the tie up
		{2.5, 0, "3"},
		{-2.5, 0, "-3"},
		{math.Copysign(0, -1), 4, "0.0000"},
		{-0.00001, 4, "-0.0000"},
		{1.005, 2, "1.00"}, // the double is below 1.005
		{1e21, 2, "1e+21"},
		{math.NaN(), 2, "NaN"},
		{0.0123, 4, "0.0123"},
	} {
		if got := domain.ToFixed(tc.v, tc.digits); got != tc.want {
			t.Errorf("ToFixed(%v, %d) = %q, want %q", tc.v, tc.digits, got, tc.want)
		}
	}
	for v, want := range map[float64]string{math.Inf(1): "Infinity", math.Inf(-1): "-Infinity", math.NaN(): "NaN", math.Copysign(0, -1): "0"} {
		if got := domain.JSNumber(v); got != want {
			t.Errorf("JSNumber(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestCanonical_MatchesNode(t *testing.T) {
	cases := loadJSText(t).Canonical
	for i, tc := range cases {
		var v any
		if err := json.Unmarshal([]byte(tc.Input), &v); err != nil {
			t.Fatalf("case %d: bad input %q: %v", i, tc.Input, err)
		}
		got, err := domain.Canonical(v)
		if err != nil {
			t.Errorf("case %d: Canonical(%s) error = %v", i, tc.Input, err)
			continue
		}
		if got != tc.Want {
			t.Errorf("case %d: Canonical(%s) = %s, want %s", i, tc.Input, got, tc.Want)
		}
	}
}

func TestCanonical_IntentKeyShape(t *testing.T) {
	type key struct {
		Title         string   `json:"title"`
		Body          string   `json:"body"`
		Branch        string   `json:"branch"`
		HeadSHA       string   `json:"head_sha"`
		PromptVersion int      `json:"prompt_version"`
		TicketRefs    []int    `json:"ticket_refs"`
		DocPaths      []string `json:"doc_paths"`
	}
	got, err := domain.Canonical(key{Title: "a <b> & \u2028 \"q\" \x01", Branch: "é", HeadSHA: "abc", PromptVersion: 2, TicketRefs: []int{3, 12}, DocPaths: []string{"a.md"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"body":"","branch":"é","doc_paths":["a.md"],"head_sha":"abc","prompt_version":2,"ticket_refs":[3,12],"title":"a <b> & ` + "\u2028" + ` \"q\" \u0001"}`
	if got != want {
		t.Errorf("Canonical(key) = %s, want %s", got, want)
	}
}

func TestCanonical_RejectsUnencodable(t *testing.T) {
	if _, err := domain.Canonical(map[string]any{"f": math.NaN()}); err == nil {
		t.Error("Canonical(NaN) error = nil, want the encoding/json error")
	}
}

func TestPrefixUnits(t *testing.T) {
	for _, tc := range []struct {
		in   string
		end  int
		want string
	}{
		{"abc", 5, "abc"}, {"abc", 0, ""}, {"abc", -1, "ab"}, {"abc", -9, ""}, {"a😀b", 2, "a�"}, {"a😀b", 3, "a😀"},
	} {
		if got := domain.PrefixUnits(tc.in, tc.end); got != tc.want {
			t.Errorf("PrefixUnits(%q, %d) = %q, want %q", tc.in, tc.end, got, tc.want)
		}
	}
}

func FuzzJSText(f *testing.F) {
	f.Add("%E4%BD%A0%zz")
	f.Add("a%2Fb")
	f.Fuzz(func(t *testing.T, s string) {
		got, err := domain.DecodeURIComponent(s)
		if err == nil && !utf8.ValidString(got) && utf8.ValidString(s) {
			t.Errorf("DecodeURIComponent(%q) = %q, invalid UTF-8 from valid input", s, got)
		}
		domain.Cut(s, 5)
		domain.CompareUnits(s, got)
	})
}
