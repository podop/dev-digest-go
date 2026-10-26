package llm

import (
	"context"
	"sync"
	"time"
)

// DefaultPriceTTL is how long live OpenRouter prices are kept.
const DefaultPriceTTL = 6 * time.Hour

// PriceBookOptions configures a PriceBook.
type PriceBookOptions struct {
	// List fetches the live OpenRouter model list; it is called in the background. An error
	// (no key, provider down) leaves the static table in charge and is retried on the next lookup.
	List func(ctx context.Context) ([]ModelInfo, error)
	// TTL is the live-price lifetime (default DefaultPriceTTL).
	TTL time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// PriceBook prices a model from the live OpenRouter list when cached, else from the static
// table, else not at all (spec FR213). A lookup never waits for the network: a cold or expired
// cache starts one background fetch and answers from the static table meanwhile.
//
// The fetch goroutine is owned by the PriceBook: Close cancels it and waits for it.
type PriceBook struct {
	list func(context.Context) ([]ModelInfo, error)
	ttl  time.Duration
	now  func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	live       map[string]price
	expires    time.Time
	refreshing bool
	closed     bool
}

// NewPriceBook builds a PriceBook. Without List it only serves the static table.
func NewPriceBook(opts PriceBookOptions) *PriceBook {
	b := &PriceBook{list: opts.List, ttl: opts.TTL, now: opts.Now, live: map[string]price{}}
	if b.ttl <= 0 {
		b.ttl = DefaultPriceTTL
	}
	if b.now == nil {
		b.now = time.Now
	}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	return b
}

// Estimate returns the cost in USD of the tokens, or nil for an unknown model.
func (b *PriceBook) Estimate(model string, tokensIn, tokensOut int) *float64 {
	b.mu.Lock()
	b.maybeRefreshLocked()
	p, ok := b.live[model]
	b.mu.Unlock()
	if ok {
		c := p.cost(tokensIn, tokensOut)
		return &c
	}
	return StaticCost(model, tokensIn, tokensOut)
}

// PerM returns the price of a model in USD per 1,000,000 input and output tokens: the live
// OpenRouter price when cached, else the static table; false for an unknown model. Like Estimate
// it never waits for the network.
func (b *PriceBook) PerM(model string) (promptPerM, completionPerM float64, ok bool) {
	b.mu.Lock()
	b.maybeRefreshLocked()
	p, live := b.live[model]
	b.mu.Unlock()
	if live {
		return p.in, p.out, true
	}
	return StaticPerM(model)
}

// PerM returns the price of a model from the Client's own price book, else the static table
// (a Client built with Options.Prices has no book); false for an unknown model.
func (c *Client) PerM(model string) (promptPerM, completionPerM float64, ok bool) {
	if c.book == nil {
		return StaticPerM(model)
	}
	return c.book.PerM(model)
}

// Refresh fetches the live prices now and waits for the answer (warm-up, tests). It reports
// the fetch error; the PriceBook keeps working from the static table either way.
func (b *PriceBook) Refresh(ctx context.Context) error {
	if b.list == nil {
		return nil
	}
	models, err := b.list(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.expires = time.Time{}
		return err
	}
	b.ingestLocked(models)
	b.expires = b.now().Add(b.ttl)
	return nil
}

// Close stops the background fetch and waits for it. It is idempotent.
func (b *PriceBook) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cancel()
	b.wg.Wait()
}

func (b *PriceBook) ingestLocked(models []ModelInfo) {
	for _, m := range models {
		if m.Pricing != nil {
			b.live[m.ID] = price{in: m.Pricing.PromptPerM, out: m.Pricing.CompletionPerM}
		}
	}
}

// maybeRefreshLocked starts a background fetch when none runs and the cache is cold or expired.
// It sets the expiry up front so concurrent lookups do not stampede.
func (b *PriceBook) maybeRefreshLocked() {
	if b.list == nil || b.closed || b.refreshing || b.now().Before(b.expires) {
		return
	}
	b.refreshing = true
	b.expires = b.now().Add(b.ttl)
	b.wg.Go(func() {
		models, err := b.list(b.ctx)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.refreshing = false
		if err != nil {
			b.expires = time.Time{} // retry on the next lookup
			return
		}
		b.ingestLocked(models)
	})
}
