package tokenizer

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// The expected counts were produced by js-tiktoken 1.0.21 (cl100k_base) from the Node
// implementation, called as encode(text, [], []) like the dev-digest adapter does.
func TestCounter_Count(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"two words", "hello world", 2},
		{"code", "export function add(a: number, b: number): number {\n  return a + b;\n}\n", 20},
		{"cjk", "你好，世界！这是一个测试。", 12},
		{"emoji and zwj sequence", "ship it 🚀🚀 👨‍👩‍👧‍👦 done", 26},
		{"whitespace runs", "a    b\t\t\tc\n\n\n\n  d", 8},
		{"digits", "order 1234567890 total", 7},
		{"contractions", "I'm sure they'll say it's fine, we've seen 'S and 'T", 18},
		// U+0085 and U+FEFF are where .NET-style and ECMAScript \s differ (regexp2 vs js-tiktoken).
		{"nel and bom between words", "a\u0085b \xef\xbb\xbf c\u00a0d", 8},
		{"nel", "a\u0085b", 4},
		{"bom between spaces", "a \xef\xbb\xbf c", 3},
		{"bom alone", "\xef\xbb\xbf", 1},
		{"bom then double space", "a\xef\xbb\xbfb  c", 5},
		{"nel between spaces", "x \u0085 y", 4},
		{"line separator", "x\u2028y", 4},
		{"mongolian vowel separator", "a\u180eb", 5},
		{"vertical tab", "p\u000bq", 3},
		{"special token literal as text", "<|endoftext|>", 7},
		{"special tokens inside text", "x <|endoftext|> y <|fim_prefix|>", 14},
	}
	c := New(nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Count(tc.text); got != tc.want {
				t.Errorf("Count(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// AC3054: a special-token literal is ordinary text, and the next count is still exact.
func TestCounter_SpecialTokenIsOrdinaryText(t *testing.T) {
	var logs bytes.Buffer
	c := New(slog.New(slog.NewTextHandler(&logs, nil)))

	special := c.Count("<|endoftext|>")
	ordinary := c.Count("<| endoftext |>") // not a special literal: the baseline tokenisation shape
	if special <= 1 {
		t.Errorf("Count(<|endoftext|>) = %d, want the literal split into several ordinary tokens", special)
	}
	if ordinary == 0 {
		t.Errorf("Count of ordinary text = 0")
	}
	if got, want := c.Count("hello world"), 2; got != want {
		t.Errorf("Count(hello world) after a special literal = %d, want %d", got, want)
	}
	if logs.Len() != 0 {
		t.Errorf("a special-token literal logged a warning: %s", logs.String())
	}
}

func TestCounter_FallbackWhenEncoderFails(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		load func() (codec, error)
		text string
		want int
	}{
		{"load error", func() (codec, error) { return nil, boom }, "abcdefgh", 2},
		{"load error, rounds up", func() (codec, error) { return nil, boom }, "abcde", 2},
		{"load error, empty", func() (codec, error) { return nil, boom }, "", 0},
		{"load error, astral counts two units", func() (codec, error) { return nil, boom }, "🚀🚀", 1},
		{"count error", func() (codec, error) { return failingCodec{boom}, nil }, "abcdefghi", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := newCounter(slog.New(slog.NewTextHandler(&logs, nil)), tc.load)
			if got := c.Count(tc.text); got != tc.want {
				t.Errorf("Count(%q) = %d, want %d", tc.text, got, tc.want)
			}
			if !strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("no warning logged: %q", logs.String())
			}
			if strings.Contains(logs.String(), tc.text) && tc.text != "" {
				t.Errorf("log contains the text: %q", logs.String())
			}
		})
	}
}

// A failing call falls back for that call only; the next count is exact again.
func TestCounter_FallbackIsPerCall(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	c := newCounter(nil, func() (codec, error) {
		return codecFunc(func(s string) (int, error) {
			calls++
			if calls == 1 {
				return 0, boom
			}
			return 42, nil
		}), nil
	})
	if got := c.Count("abcdefgh"); got != 2 {
		t.Errorf("first Count = %d, want the estimate 2", got)
	}
	if got := c.Count("abcdefgh"); got != 42 {
		t.Errorf("second Count = %d, want 42 from the encoder", got)
	}
}

func TestCounter_ConcurrentUse(t *testing.T) {
	c := New(nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				if got := c.Count("hello world"); got != 2 {
					t.Errorf("Count(hello world) = %d, want 2", got)
					return
				}
			}
		})
	}
	wg.Wait()
}

type failingCodec struct{ err error }

func (f failingCodec) Count(string) (int, error) { return 0, f.err }

type codecFunc func(string) (int, error)

func (f codecFunc) Count(s string) (int, error) { return f(s) }
