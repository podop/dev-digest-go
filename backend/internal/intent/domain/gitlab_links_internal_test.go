package domain

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// refBeforeReference is the first, quadratic refBefore: the oracle the linear one is checked against.
func refBeforeReference(text string, hash, floor int) (int, bool) {
	i := hash
	for i > floor && isRefByte(text[i-1]) {
		i--
	}
	for s := i; s < hash; s++ {
		if text[s] == '/' || s > 0 && isWordOrSlash(text[s-1]) || s > 0 && text[s-1] == '.' {
			continue
		}
		p := text[s:hash]
		if strings.Contains(p, "//") || strings.HasSuffix(p, "/") || strings.Count(p, "/")+1 > maxRefSegments {
			continue
		}
		return s, true
	}
	return 0, false
}

func checkRefBefore(t *testing.T, text string) {
	t.Helper()
	for hash := 0; hash < len(text); hash++ {
		if text[hash] != '#' {
			continue
		}
		for _, floor := range []int{0, max(0, hash-3), max(0, hash-25)} {
			gotS, gotOK := refBefore(text, hash, floor)
			wantS, wantOK := refBeforeReference(text, hash, floor)
			if gotS != wantS || gotOK != wantOK {
				t.Fatalf("refBefore(%q, %d, %d) = %d, %v, want %d, %v", text, hash, floor, gotS, gotOK, wantS, wantOK)
			}
		}
	}
}

func TestRefBefore_MatchesTheReferenceImplementation(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	alphabet := []string{"a", "b", "-", "/", "/", ".", "_", "9", " ", "#1", "#"}
	for range 20000 {
		var b strings.Builder
		for range rng.IntN(40) + 1 {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		b.WriteString("#1")
		checkRefBefore(t, b.String())
	}
	// Segment-count edges: 20, 21 and 22 slashes before the hash.
	for _, n := range []int{19, 20, 21, 22, 23} {
		checkRefBefore(t, strings.Repeat("a/", n)+"b#1")
		checkRefBefore(t, "-"+strings.Repeat("a/", n)+"b#1")
		checkRefBefore(t, strings.Repeat("a-", n)+"b#1")
	}
}

func FuzzRefBefore(f *testing.F) {
	for _, seed := range []string{"a/b#1", "a//b#1", "-a-a/#1", "x.y#1", "a-b/c-d#1", strings.Repeat("a/", 22) + "b#1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 300 {
			t.Skip()
		}
		checkRefBefore(t, text)
	})
}
