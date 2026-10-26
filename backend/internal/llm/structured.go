package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/llm/custom"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/keynames"
	"dev-digest-go/backend/internal/platform/logging"
	"dev-digest-go/backend/internal/platform/retry"
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
	for _, name := range []string{keynames.OpenAIKey, keynames.AnthropicKey, keynames.OpenRouterKey, keynames.GitHubToken} {
		s = redactValue(s, c.keys.Get(name))
	}
	if c.extraRedactions != nil {
		for _, secret := range longestFirst(c.extraRedactions()) {
			if len(secret) >= minRedactLen {
				s = redactValue(s, secret)
			}
		}
	}
	return s
}

// minRedactLen is the shortest extra secret Redact masks (NFR7 uses the same bound for the runner).
const minRedactLen = 8

func longestFirst(in []string) []string {
	return slices.SortedFunc(slices.Values(in), func(a, b string) int { return len(b) - len(a) })
}

func redactValue(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, logging.Redacted)
}

// scrub removes the key used by a call (it may have been rotated since), every stored key and the
// call's own secrets: a custom provider's key and extra-header values (NFR6).
func (c *Client) scrub(s, key string, secrets []string) string {
	// Longest first, so a secret that contains another one is not left half-redacted.
	for _, secret := range longestFirst(secrets) {
		s = redactValue(s, secret)
	}
	return c.Redact(redactValue(s, key))
}

// Ready reports whether a call to the built-in provider p can be made now: an adapter is wired and
// its key, when it needs one, is stored. The error is the one Structured returns first, e.g.
// "OPENAI_API_KEY is not configured". The key is read now, like every call does. A custom provider
// belongs to a workspace and is checked by ReadyIn; here it is never ready.
func (c *Client) Ready(p Provider) error {
	if p.IsCustom() {
		return customNotConfigured(p)
	}
	ad, err := c.adapter(p)
	if err != nil {
		return err
	}
	_, err = c.keyFor(p, ad)
	return err
}

// ReadyIn is Ready for a provider of workspace ws: a built-in provider is checked as Ready does, a
// custom one is ready when the workspace defines it (FR9). A custom provider needs no key, so a
// definition is all it takes; no request is made.
func (c *Client) ReadyIn(ctx context.Context, ws string, p Provider) error {
	if !p.IsCustom() {
		return c.Ready(p)
	}
	_, err := c.resolveCustom(ctx, ws, p)
	return err
}

func customNotConfigured(p Provider) error {
	return apperr.Config(fmt.Sprintf("Custom provider %s is not configured", string(p)))
}

// resolveCustom asks the resolver for the call of custom provider p of workspace ws. No resolver,
// or a provider the workspace does not define, is the FR9 config error.
func (c *Client) resolveCustom(ctx context.Context, ws string, p Provider) (CustomCall, error) {
	if c.custom == nil {
		return CustomCall{}, customNotConfigured(p)
	}
	call, err := c.custom.Resolve(ctx, ws, p)
	switch {
	case errors.Is(err, ErrCustomNotConfigured):
		return CustomCall{}, customNotConfigured(p)
	case err != nil && ctx.Err() != nil:
		return CustomCall{}, fmt.Errorf("resolve custom provider %s: %w", string(p), ctx.Err())
	case err != nil:
		return CustomCall{}, apperr.Internal(fmt.Sprintf("could not resolve custom provider %s", string(p))).WithCause(err)
	case call.Adapter == nil:
		return CustomCall{}, apperr.Internal(fmt.Sprintf("custom provider %s has no adapter", string(p)))
	}
	if !call.Mode.Valid() {
		call.Mode = custom.DefaultMode
	}
	return call, nil
}

// workspaceOf is the workspace a call is charged to; "" for a call without a usable Charge.
func workspaceOf(ch *Charge) string {
	if ch == nil {
		return ""
	}
	return ch.WorkspaceID
}

// target resolves what a call talks to: the adapter of a built-in provider, or the per-call
// adapter of a custom one (cc non-nil).
func (c *Client) target(ctx context.Context, ws string, p Provider) (Adapter, *CustomCall, error) {
	if p.IsCustom() {
		call, err := c.resolveCustom(ctx, ws, p)
		if err != nil {
			return nil, nil, err
		}
		return call.Adapter, &call, nil
	}
	ad, err := c.adapter(p)
	return ad, nil, err
}

// displayName is the provider's name in error texts: a custom provider's own name.
func displayName(p Provider, cc *CustomCall) string {
	if cc != nil && cc.Name != "" {
		return cc.Name
	}
	return p.DisplayName()
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

// keyFor reads a built-in provider's key now; a provider that needs none (the mock) gets "". A
// custom provider's adapter carries its own key, so Structured never asks it for one.
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
	ad, cc, err := c.target(ctx, workspaceOf(req.Charge), req.Provider)
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
	if c.recorder != nil && !req.Charge.complete() {
		c.log.WarnContext(ctx, "llm call has no usable spend charge; its usage will not be recorded", "schema", req.SchemaName, "feature", req.Feature)
	}
	var key string
	var secrets []string
	var mode custom.Mode
	messages := slices.Clone(req.Messages)
	if cc != nil {
		secrets, mode = cc.Secrets, cc.Mode
		if mode != custom.ModeStrict {
			messages = withSchemaBlock(messages, strict)
		}
	} else if key, err = c.keyFor(req.Provider, ad); err != nil {
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

	name := displayName(req.Provider, cc)
	label := fmt.Sprintf("%s structured call for %s", name, req.SchemaName)
	budgetCtx, cancel := context.WithTimeout(ctx, c.budget)
	defer cancel()

	trace := newTrace(cc, req.Provider, "complete")
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
			Temperature: req.Temperature, MaxTokens: req.MaxTokens, SessionID: req.SessionID, Mode: mode,
		}, req.SchemaName, trace, func(a Attempt, res AttemptResult) {
			// The provider answered with something unusable: its tokens were spent all the same.
			// Reported here, per response, so that a retry of this request cannot lose it.
			usage := res.Usage
			if usage == nil {
				usage = c.estimateUsage(req, a.Messages, "")
			}
			c.reportUsage(ctx, req, cc, attemptRecord{reported: res.CostUSD, usage: *usage, estimated: res.Usage == nil})
		})
		if err != nil {
			return Result{}, c.fail(ctx, budgetCtx, label, key, cc, err)
		}

		lastRaw = res.Raw
		usage := res.Usage
		if usage == nil {
			usage = c.estimateUsage(req, messages, res.Raw)
		}
		tokensIn += usage.TokensIn
		tokensOut += usage.TokensOut
		attempts = append(attempts, attemptRecord{reported: res.CostUSD, usage: *usage, estimated: res.Usage == nil})
		c.reportUsage(ctx, req, cc, attempts[len(attempts)-1])

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
				CostUSD: c.totalCost(req, cc, attempts),
				Raw:     lastRaw, Attempts: attempt,
			}, nil
		}

		if attempt > reprompts {
			// Redact before cutting: a cut may leave half a key that no longer matches. The key
			// of this call may have been rotated since, hence scrub and not only Redact.
			issues := truncateUTF16(c.scrub(lastIssues, key, secrets), issuesSnippet)
			raw := truncateUTF16(c.scrub(lastRaw, key, secrets), rawSnippet)
			msg := fmt.Sprintf("%s structured output failed schema validation for %s after %d attempt(s). Last issues:\n%s\nLast raw output (%d chars): %s",
				name, req.SchemaName, attempt, issues, UTF16Len(lastRaw), raw)
			return Result{}, apperr.ExternalService(c.scrub(msg, key, secrets))
		}
		messages = append(messages, Message{Role: Assistant, Content: res.Raw}, Message{Role: User, Content: reprompt})
	}
}

// send makes one logical request: retried while the failure is transient and the budget lasts.
// answered is called for every request that failed although the provider answered it (a 200 the
// adapter could not use), also when the request is retried afterwards.
func (c *Client) send(budgetCtx context.Context, ad Adapter, pol Policy, key, provider string, reqTimeout time.Duration, a Attempt, schema string, trace *requestTrace, answered func(Attempt, AttemptResult)) (AttemptResult, error) {
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
		start := time.Now()
		res, err := ad.Complete(rctx, key, a)
		if err != nil && res.Answered {
			answered(a, res)
		}
		if err != nil && own && rctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			err = &requestTimeoutError{what: fmt.Sprintf("%s request for %s", provider, schema), timeout: reqTimeout}
		}
		trace.log(ctx, c.log, err, time.Since(start))
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
func (c *Client) fail(parent, budget context.Context, label, key string, cc *CustomCall, err error) error {
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
	var secrets []string
	if cc != nil {
		secrets = cc.Secrets
	}
	// A custom gateway's text is untrusted: besides the exact secrets, a masked or partial copy of
	// the key or of a header value must not survive either (NFR1).
	text := err.Error()
	if cc != nil {
		text = custom.Scrub(text, secrets, logging.Redacted)
	}
	msg := c.scrub(text, key, secrets)
	if hint := strictHint(cc, err); hint != "" {
		msg += "\n" + hint
	}
	return apperr.ExternalService(msg)
}

// strictHint is the FR33 advice for a generation that a custom provider in strict mode refused with
// HTTP 400 or 422; "" in every other case. The call is never repeated in another mode.
func strictHint(cc *CustomCall, err error) string {
	if cc == nil || cc.Mode != custom.ModeStrict {
		return ""
	}
	if code := statusOf(err); code != 400 && code != 422 {
		return ""
	}
	name := cc.Name
	if name == "" {
		name = "this provider"
	}
	alt := "Prompt only"
	if cc.Protocol == custom.ProtocolOpenAI {
		alt = "JSON object or Prompt only"
	}
	return fmt.Sprintf("If this gateway does not support strict structured output, set Structured output of %s to %s.", name, alt)
}

// statusOf is the HTTP status an adapter error carries, or 0.
func statusOf(err error) int {
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		return sc.StatusCode()
	}
	return 0
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

// reportUsage hands one response's usage to the spend Recorder (when the call carries a usable
// Charge) and to req.OnUsage: the provider's cost, else the price book's estimate for that attempt
// (nil = unpriced). Both only observe; a panic in either must not fail the call it observes, nor
// keep the other from running.
func (c *Client) reportUsage(ctx context.Context, req Request, cc *CustomCall, a attemptRecord) {
	recording := c.recorder != nil && req.Charge.complete()
	if !recording && req.OnUsage == nil {
		return
	}
	cost := c.attemptCost(req, cc, a)
	if recording {
		c.observe(ctx, req, "llm usage recorder panicked", func() {
			c.recorder.RecordUsage(ctx, RecordedUsage{
				Charge: *req.Charge, Provider: req.Provider, Model: req.Model, Tokens: a.usage, CostUSD: cost,
			})
		})
	}
	if req.OnUsage != nil {
		c.observe(ctx, req, "llm usage observer panicked", func() {
			req.OnUsage(AttemptUsage{TokensIn: a.usage.TokensIn, TokensOut: a.usage.TokensOut, CostUSD: cost})
		})
	}
}

// observe runs fn and turns its panic into a warning.
func (c *Client) observe(ctx context.Context, req Request, msg string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.log.WarnContext(ctx, msg, "schema", req.SchemaName, "panic", fmt.Sprint(r))
		}
	}()
	fn()
}

// attemptRecord is what one attempt cost: the provider's own figure (nil = none) and its tokens.
type attemptRecord struct {
	reported *float64
	usage    Usage
	// estimated says the response carried no usage and the tokens are the client's estimate.
	estimated bool
}

// attemptCost is the cost of one attempt. A built-in provider: its own figure, else the price
// book's estimate (nil = unpriced; also nil for estimated tokens under PriceReportedUsageOnly). A custom provider (FR12): its own figure when a finite number
// >= 0, else the manual price applied to the tokens the response reported, else nil; the price
// books are never consulted.
func (c *Client) attemptCost(req Request, cc *CustomCall, a attemptRecord) *float64 {
	if cc == nil {
		if a.reported != nil {
			return a.reported
		}
		if a.estimated && c.reportedOnly {
			return nil
		}
		return c.prices.Estimate(req.Model, a.usage.TokensIn, a.usage.TokensOut)
	}
	if validCost(a.reported) {
		return a.reported
	}
	if a.estimated || cc.Price == nil {
		return nil
	}
	if cost := cc.Price.Estimate(req.Model, a.usage.TokensIn, a.usage.TokensOut); validCost(cost) {
		return cost
	}
	return nil
}

// maxAttemptCostUSD is the largest cost one attempt may report. A gateway's figure is untrusted
// text: a larger one is unknown, so it cannot overflow numeric(12,6) of agent_runs once the
// attempts and the agents of a run are summed (the ledger treats >= 1e12 and CI ingest >= 1e6 as
// unknown, too).
const maxAttemptCostUSD = 1e5

// validCost reports a non-nil, finite cost within [0, maxAttemptCostUSD].
func validCost(f *float64) bool {
	return f != nil && !math.IsNaN(*f) && !math.IsInf(*f, 0) && *f >= 0 && *f <= maxAttemptCostUSD
}

// totalCost is the call's cost. OpenRouter, a custom provider (FR12 is per attempt) and any
// provider that reports a cost per attempt, sums the attempts (the provider's figure, else the price book; one unknown makes the sum
// unknown); OpenAI and Anthropic price the summed tokens once (spec FR212).
func (c *Client) totalCost(req Request, cc *CustomCall, attempts []attemptRecord) *float64 {
	perAttempt := cc != nil || req.Provider == OpenRouter
	tokensIn, tokensOut := 0, 0
	estimated := false
	for _, a := range attempts {
		perAttempt = perAttempt || a.reported != nil
		estimated = estimated || a.estimated
		tokensIn += a.usage.TokensIn
		tokensOut += a.usage.TokensOut
	}
	if !perAttempt {
		if estimated && c.reportedOnly {
			return nil // the sum holds estimated tokens, which are never priced
		}
		return c.prices.Estimate(req.Model, tokensIn, tokensOut)
	}
	sum := 0.0
	for _, a := range attempts {
		cost := c.attemptCost(req, cc, a)
		if cost == nil {
			return nil
		}
		sum += *cost
	}
	return &sum
}

// schemaPromptBlock is the instruction block of the modes that carry the schema in the prompt
// (FR33): the schema text strict mode would send and the demand for exactly one JSON object.
func schemaPromptBlock(schema json.RawMessage) string {
	return "Answer with exactly one JSON object that matches this JSON schema, and nothing else (no prose, no code fences):\n" + string(schema)
}

// withSchemaBlock returns msgs with the schema block appended to the first system message, or
// prepended as a system message of its own when there is none. msgs itself is not changed.
func withSchemaBlock(msgs []Message, schema json.RawMessage) []Message {
	block := schemaPromptBlock(schema)
	out := slices.Clone(msgs)
	for i, m := range out {
		if m.Role == System {
			out[i].Content = m.Content + "\n\n" + block
			return out
		}
	}
	return append([]Message{{Role: System, Content: block}}, out...)
}

// requestTrace logs one line per HTTP request of a custom-provider call (NFR6): provider id, base
// URL host, operation, status or error class, attempt and latency. It never holds or logs a key,
// a header value, the prompt or an answer. A nil trace (a built-in provider) logs nothing.
type requestTrace struct {
	provider, host, operation string
	attempt                   int
}

func newTrace(cc *CustomCall, p Provider, operation string) *requestTrace {
	if cc == nil {
		return nil
	}
	return &requestTrace{provider: string(p), host: cc.Host, operation: operation}
}

// log records the request that just ended with err. The attempt counts every request of the call,
// retries and re-prompts included; requests run one after the other, so no lock is needed.
func (t *requestTrace) log(ctx context.Context, l *slog.Logger, err error, took time.Duration) {
	if t == nil {
		return
	}
	t.attempt++
	l.InfoContext(ctx, "custom provider request",
		"provider", t.provider, "host", t.host, "operation", t.operation,
		"status", requestStatus(err), "attempt", t.attempt, "latency_ms", took.Milliseconds())
}

// requestStatus is the HTTP status of a failed request, "2xx" for a success, else the error class.
func requestStatus(err error) string {
	var (
		rt *requestTimeoutError
		ne net.Error
	)
	switch {
	case err == nil:
		return "2xx"
	case statusOf(err) != 0:
		return strconv.Itoa(statusOf(err))
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &rt), errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	}
	return "error"
}
