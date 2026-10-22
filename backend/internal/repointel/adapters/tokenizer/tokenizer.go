// Package tokenizer counts cl100k_base tokens for the repo map. The vocabulary is embedded in
// the tiktoken-go/tokenizer module (about 15 MB in the binary) and loaded on first use.
package tokenizer

import (
	"log/slog"
	"sync"

	tiktoken "github.com/tiktoken-go/tokenizer"

	"dev-digest-go/backend/internal/repointel/domain"
)

// codec is the part of a tiktoken codec the counter uses.
type codec interface {
	Count(text string) (int, error)
}

// Counter counts cl100k_base tokens. Special-token literals such as <|endoftext|> are counted as
// ordinary text, as js-tiktoken does with allowed and disallowed special sets empty (FR3027). It
// is safe for concurrent use.
type Counter struct {
	log  *slog.Logger
	load func() (codec, error)

	once sync.Once
	c    codec
	err  error
}

var _ domain.TokenCounter = (*Counter)(nil)

// New returns a Counter. A nil log discards warnings.
func New(log *slog.Logger) *Counter {
	return newCounter(log, func() (codec, error) { return tiktoken.Get(tiktoken.Cl100kBase) })
}

func newCounter(log *slog.Logger, load func() (codec, error)) *Counter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Counter{log: log, load: load}
}

// Count returns the number of cl100k_base tokens of text. Only when the encoder cannot be used
// does it return ceil(UTF-16 length / 4) and log a warning; the next call tries the encoder again.
func (c *Counter) Count(text string) int {
	c.once.Do(func() { c.c, c.err = c.load() })
	if c.err != nil {
		c.log.Warn("repo-intel tokenizer unavailable, estimating token count", "error", c.err.Error())
		return approx(text)
	}
	n, err := c.c.Count(text)
	if err != nil {
		c.log.Warn("repo-intel tokenizer failed, estimating token count", "error", err.Error())
		return approx(text)
	}
	return n
}

// approx is ceil(UTF-16 length / 4), the estimate used when the encoder cannot be used.
func approx(text string) int {
	return (domain.UTF16Len(text) + 3) / 4
}
