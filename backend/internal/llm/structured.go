package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/logging"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/platform/secrets"
)

// Diagnostic snippet sizes of the final schema-failure text (spec 7.2).
const (
	issuesSnippet = 600
	rawSnippet    = 400
)

// Redact replaces every configured provider key and the GitHub token in s with [REDACTED]. The
// logging package redacts by attribute name only, so any text that may carry a provider's
// error message goes through here first (decision D219).
func (c *Client) Redact(s string) string {
	for _, name := range []string{secrets.OpenAIKey, secrets.AnthropicKey, secrets.OpenRouterKey, secrets.GitHubToken} {
		s = redactValue(s, c.keys.Get(name))
	}
	return s
}

func redactValue(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, logging.Redacted)
}

// scrub removes the key used by a call (it may have been rotated since) and every stored key.
func (c *Client) scrub(s, key string) string { return c.Redact(redactValue(s, key)) }

// Ready reports whether a call to p can be made now: an adapter is wired and its key, when it
// needs one, is stored. The error is the one Structured returns first, e.g. "OPENAI_API_KEY is
// not configured". The key is read now, like every call does.
func (c *Client) Ready(p Provider) error {
	ad, err := c.adapter(p)
	if err != nil {
		return err
	}
	_, err = c.keyFor(p, ad)
	return err
}

func (c *Client) adapter(p Provider) (Adapter, error) {
	if !p.Valid() {
		return nil, apperr.Internal(fmt.Sprintf("unknown LLM provider %q", string(p)))
	}
	ad := c.adapters[p]
	if ad == nil {
		return nil, apperr.Internal(fmt.Sprintf("no adapter configured for LLM provider %q", string(p)))
	}
	return ad, nil
}

// keyFor reads the provider's key now; a provider that needs none (the mock) gets "".
func (c *Client) keyFor(p Provider, ad Adapter) (string, error) {
	if !ad.RequiresKey() {
		return "", nil
	}
	name := p.KeyName()
	key := c.keys.Get(name)
	if key == "" {
		return "", apperr.Config(name + " is not configured")
	}
	return key, nil
}

// requestTimeoutError is one HTTP request that outlived its own timeout. It is deliberately not
// a net.Error and does not wrap the context error, so it is never retried.
type requestTimeoutError struct {
	what    string
	timeout time.Duration
}

func (e *requestTimeoutError) Error() string {
	return fmt.Sprintf("%s timed out after %dms", e.what, e.timeout.Milliseconds())
}

// Structured asks the model for JSON matching req.Schema (spec FR205, FR209–FR212, FR215).
//
// Every request, retry and re-prompt shares one budget (360 s by default). A bad answer is
// re-prompted up to the reprompt count; the final failure is an external_service error that
// names the last issues and a cut of the raw output. Failures are apperr errors whose texts
// are scrubbed of keys; a cancelled caller context yields an error wrapping ctx.Err().
func (c *Client) Structured(ctx context.Context, req Request) (Result, error) {
	ad, err := c.adapter(req.Provider)
	if err != nil {
		return Result{}, err
	}
	if req.Model == "" || req.SchemaName == "" || len(req.Messages) == 0 {
		return Result{}, apperr.Internal("structured request needs a model, a schema name and messages")
	}
	strict, err := StrictSchema(req.Schema)
	if err != nil {
		return Result{}, apperr.Internal(err.Error())
	}
	val, err := newValidator(req.Schema)
	if err != nil {
		return Result{}, apperr.Internal(err.Error())
	}

	c.logPrompt(ctx, req)
	key, err := c.keyFor(req.Provider, ad)
	if err != nil {
		return Result{}, err
	}

	pol := ad.Policy()
	reqTimeout := req.Timeout
	if reqTimeout <= 0 {
		reqTimeout = pol.RequestTimeout
	}
	reprompts := DefaultReprompts
	switch {
	case req.Reprompts < 0:
		reprompts = 0
	case req.Reprompts > 0:
		reprompts = req.Reprompts
	}

	name := req.Provider.DisplayName()
	label := fmt.Sprintf("%s structured call for %s", name, req.SchemaName)
	budgetCtx, cancel := context.WithTimeout(ctx, c.budget)
	defer cancel()

	messages := slices.Clone(req.Messages)
	var (
		tokensIn, tokensOut int
		attempts            []attemptRecord
		lastRaw, lastIssues string
	)
	for attempt := 1; ; attempt++ {
		if err := c.stopped(ctx, budgetCtx, label); err != nil {
			return Result{}, err
		}
		res, err := c.send(budgetCtx, ad, pol, key, name, reqTimeout, Attempt{
			Model: req.Model, Messages: messages, SchemaName: req.SchemaName, Schema: strict,
			Temperature: req.Temperature, MaxTokens: req.MaxTokens, SessionID: req.SessionID,
		}, req.SchemaName)
		if err != nil {
			if res.Answered {
				// The provider answered with something unusable: its tokens were spent all the same.
				usage := res.Usage
				if usage == nil {
					usage = c.estimateUsage(req, messages, "")
				}
				c.reportUsage(ctx, req, attemptRecord{reported: res.CostUSD, usage: *usage})
			}
			return Result{}, c.fail(ctx, budgetCtx, label, key, err)
		}

		lastRaw = res.Raw
		usage := res.Usage
		if usage == nil {
			usage = c.estimateUsage(req, messages, res.Raw)
		}
		tokensIn += usage.TokensIn
		tokensOut += usage.TokensOut
		attempts = append(attempts, attemptRecord{reported: res.CostUSD, usage: *usage})
		c.reportUsage(ctx, req, attempts[len(attempts)-1])

		text, perr := extractJSON(res.Raw)
		var reprompt string
		if perr != nil {
			lastIssues = "Output was not valid JSON: " + perr.Error()
			reprompt = lastIssues + "\nReturn ONLY a single valid JSON object matching the schema, no prose."
		} else if issues := val.validate(text); len(issues) > 0 {
			lines := make([]string, len(issues))
			for i, is := range issues {
				lines[i] = is.String()
			}
			lastIssues = strings.Join(lines, "\n")
			reprompt = "Your JSON did not match the required schema. Fix these and return ONLY valid JSON:\n" + lastIssues
		} else {
			return Result{
				Data: []byte(text), Model: req.Model, TokensIn: tokensIn, TokensOut: tokensOut,
				CostUSD: c.totalCost(req, attempts),
				Raw:     lastRaw, Attempts: attempt,
			}, nil
		}

		if attempt > reprompts {
			msg := fmt.Sprintf("%s structured output failed schema validation for %s after %d attempt(s). Last issues:\n%s\nLast raw output (%d chars): %s",
				name, req.SchemaName, attempt, truncateUTF16(lastIssues, issuesSnippet), UTF16Len(lastRaw), truncateUTF16(lastRaw, rawSnippet))
			return Result{}, apperr.ExternalService(c.scrub(msg, key))
		}
		messages = append(messages, Message{Role: Assistant, Content: res.Raw}, Message{Role: User, Content: reprompt})
	}
}

// send makes one logical request: retried while the failure is transient and the budget lasts.
func (c *Client) send(budgetCtx context.Context, ad Adapter, pol Policy, key, provider string, reqTimeout time.Duration, a Attempt, schema string) (AttemptResult, error) {
	opts := c.backoff
	opts.Retries = pol.Retries
	return retry.Value(budgetCtx, opts, func(ctx context.Context) (AttemptResult, error) {
		rctx := ctx
		own := false
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > reqTimeout {
			var cancel context.CancelFunc
			rctx, cancel = context.WithTimeout(ctx, reqTimeout)
			defer cancel()
			own = true
		}
		res, err := ad.Complete(rctx, key, a)
		if err != nil && own && rctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			err = &requestTimeoutError{what: fmt.Sprintf("%s request for %s", provider, schema), timeout: reqTimeout}
		}
		return res, err
	})
}

// stopped reports a cancelled caller or an exhausted budget before another request is made.
func (c *Client) stopped(parent, budget context.Context, label string) error {
	if err := parent.Err(); err != nil {
		return fmt.Errorf("%s was cancelled: %w", label, err)
	}
	if budget.Err() != nil {
		return c.budgetError(label)
	}
	return nil
}

func (c *Client) budgetError(label string) error {
	return apperr.ExternalService(fmt.Sprintf("%s exceeded its total budget of %dms", label, c.budget.Milliseconds()))
}

// fail turns the error of a request into the call's error: cancellation and budget first, then
// the provider's failure with keys scrubbed.
func (c *Client) fail(parent, budget context.Context, label, key string, err error) error {
	if perr := parent.Err(); perr != nil {
		return fmt.Errorf("%s was cancelled: %w", label, perr)
	}
	if budget.Err() != nil {
		return c.budgetError(label)
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return err
	}
	return apperr.ExternalService(c.scrub(err.Error(), key))
}

// estimateUsage covers a response without usage: ceil(chars / 4) of what was sent and of the
// answer, with a warning (spec FR212).
func (c *Client) estimateUsage(req Request, messages []Message, output string) *Usage {
	chars := 0
	for i, m := range messages {
		if i > 0 {
			chars++ // the "\n" the messages are joined with
		}
		chars += UTF16Len(m.Content)
	}
	u := &Usage{TokensIn: ceilTokens(chars), TokensOut: ceilTokens(UTF16Len(output))}
	c.log.Warn(fmt.Sprintf("%s (%s) returned no usage; estimated %d in / %d out tokens", req.Model, req.SchemaName, u.TokensIn, u.TokensOut))
	return u
}

// reportUsage hands one response's usage to req.OnUsage: the provider's cost, else the price
// book's estimate for that attempt (nil = unpriced). The hook only observes; a panic in it must
// not fail the call it observes.
func (c *Client) reportUsage(ctx context.Context, req Request, a attemptRecord) {
	if req.OnUsage == nil {
		return
	}
	cost := a.reported
	if cost == nil {
		cost = c.prices.Estimate(req.Model, a.usage.TokensIn, a.usage.TokensOut)
	}
	defer func() {
		if r := recover(); r != nil {
			c.log.WarnContext(ctx, "llm usage observer panicked", "schema", req.SchemaName, "panic", fmt.Sprint(r))
		}
	}()
	req.OnUsage(AttemptUsage{TokensIn: a.usage.TokensIn, TokensOut: a.usage.TokensOut, CostUSD: cost})
}

// attemptRecord is what one attempt cost: the provider's own figure (nil = none) and its tokens.
type attemptRecord struct {
	reported *float64
	usage    Usage
}

// totalCost is the call's cost. OpenRouter, and any provider that reports a cost per attempt,
// sums the attempts (the provider's figure, else the price book; one unknown makes the sum
// unknown); OpenAI and Anthropic price the summed tokens once (spec FR212).
func (c *Client) totalCost(req Request, attempts []attemptRecord) *float64 {
	perAttempt := req.Provider == OpenRouter
	tokensIn, tokensOut := 0, 0
	for _, a := range attempts {
		perAttempt = perAttempt || a.reported != nil
		tokensIn += a.usage.TokensIn
		tokensOut += a.usage.TokensOut
	}
	if !perAttempt {
		return c.prices.Estimate(req.Model, tokensIn, tokensOut)
	}
	sum := 0.0
	for _, a := range attempts {
		cost := a.reported
		if cost == nil {
			cost = c.prices.Estimate(req.Model, a.usage.TokensIn, a.usage.TokensOut)
		}
		if cost == nil {
			return nil
		}
		sum += *cost
	}
	return &sum
}
