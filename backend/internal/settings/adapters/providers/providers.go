// Package providers checks LLM provider keys by listing models over plain net/http. Base URLs
// are options so tests point them at httptest servers.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"dev-digest-go/backend/internal/platform/logging"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/settings/app"
)

// Defaults (Node parity: OpenAI and Anthropic through their SDKs with retries, OpenRouter a
// plain fetch with a 15 s timeout and no retry).
const (
	DefaultOpenAIBaseURL     = "https://api.openai.com/v1"
	DefaultAnthropicBaseURL  = "https://api.anthropic.com/v1"
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

	DefaultTimeout           = 30 * time.Second
	DefaultOpenRouterTimeout = 15 * time.Second

	anthropicVersion = "2023-06-01"
	maxBodyBytes     = 16 << 20
	maxMessageBytes  = 300
)

// Options configures a Client. Zero fields take the defaults above.
type Options struct {
	OpenAIBaseURL     string
	AnthropicBaseURL  string
	OpenRouterBaseURL string
	HTTPClient        *http.Client
	// Timeout bounds each OpenAI and Anthropic request.
	Timeout time.Duration
	// OpenRouterTimeout bounds the OpenRouter request.
	OpenRouterTimeout time.Duration
	// Retry is the policy for OpenAI and Anthropic; nil means retry.Default().
	Retry *retry.Options
}

// Client lists provider models. It is safe for concurrent use.
type Client struct {
	openAI, anthropic, openRouter string
	http                          *http.Client
	timeout, openRouterTimeout    time.Duration
	retry                         retry.Options
}

// New builds a Client.
func New(opts Options) *Client {
	c := &Client{
		openAI:            trimmedOr(opts.OpenAIBaseURL, DefaultOpenAIBaseURL),
		anthropic:         trimmedOr(opts.AnthropicBaseURL, DefaultAnthropicBaseURL),
		openRouter:        trimmedOr(opts.OpenRouterBaseURL, DefaultOpenRouterBaseURL),
		http:              opts.HTTPClient,
		timeout:           opts.Timeout,
		openRouterTimeout: opts.OpenRouterTimeout,
		retry:             retry.Default(),
	}
	// Never follow a provider's redirect: Go keeps custom headers such as Anthropic's x-api-key
	// on a cross-host redirect, so the key would reach another host. Copy, don't mutate, an
	// injected client.
	hc := http.Client{}
	if c.http != nil {
		hc = *c.http
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &hc
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.openRouterTimeout <= 0 {
		c.openRouterTimeout = DefaultOpenRouterTimeout
	}
	if opts.Retry != nil {
		c.retry = *opts.Retry
	}
	return c
}

func trimmedOr(v, def string) string {
	if v = strings.TrimRight(v, "/"); v != "" {
		return v
	}
	return def
}

// statusError is a non-2xx provider response; retry.IsTransient reads StatusCode.
type statusError struct {
	provider string
	status   int
	message  string
}

func (e *statusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("%s /models returned %d", e.provider, e.status)
	}
	return fmt.Sprintf("%s /models returned %d: %s", e.provider, e.status, e.message)
}

func (e *statusError) StatusCode() int { return e.status }

// CountModels is ListModels reduced to its Count.
func (c *Client) CountModels(ctx context.Context, provider app.Provider, key string) (int, error) {
	list, err := c.ListModels(ctx, provider, key)
	return list.Count, err
}

// ListModels makes one model-list GET (retried on transient failures for OpenAI and Anthropic,
// as before) and returns every listed id plus the count of usable ones: for OpenAI the ids
// starting with gpt or containing o1 or o3, otherwise all of them. Anthropic asks for
// limit=1000 (its maximum) so one page holds the whole list.
func (c *Client) ListModels(ctx context.Context, provider app.Provider, key string) (app.ModelList, error) {
	switch provider {
	case app.OpenAI:
		ids, err := c.withRetry(ctx, func(ctx context.Context) ([]string, error) {
			return c.listIDs(ctx, "OpenAI", c.openAI+"/models", key, map[string]string{"Authorization": "Bearer " + key}, c.timeout)
		})
		if err != nil {
			return app.ModelList{}, err
		}
		n := 0
		for _, id := range ids {
			if strings.HasPrefix(id, "gpt") || strings.Contains(id, "o1") || strings.Contains(id, "o3") {
				n++
			}
		}
		return app.ModelList{IDs: ids, Count: n}, nil
	case app.Anthropic:
		ids, err := c.withRetry(ctx, func(ctx context.Context) ([]string, error) {
			return c.listIDs(ctx, "Anthropic", c.anthropic+"/models?limit=1000", key,
				map[string]string{"x-api-key": key, "anthropic-version": anthropicVersion}, c.timeout)
		})
		return app.ModelList{IDs: ids, Count: len(ids)}, err
	case app.OpenRouter:
		ids, err := c.listIDs(ctx, "OpenRouter", c.openRouter+"/models", key, map[string]string{"Authorization": "Bearer " + key}, c.openRouterTimeout)
		return app.ModelList{IDs: ids, Count: len(ids)}, err
	default:
		return app.ModelList{}, fmt.Errorf("provider %q has no model list", provider)
	}
}

func (c *Client) withRetry(ctx context.Context, fn func(context.Context) ([]string, error)) ([]string, error) {
	return retry.Value(ctx, c.retry, fn)
}

// listIDs GETs url and returns data[].id of the 2xx JSON body.
func (c *Client) listIDs(ctx context.Context, name, url, key string, headers map[string]string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%s /models: %w", name, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dev-digest-go")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport error carries the URL, never the headers.
		return nil, fmt.Errorf("%s /models: %w", name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%s /models: read response: %w", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &statusError{provider: name, status: resp.StatusCode, message: apiMessage(name, key, resp.StatusCode, body)}
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New(name + " /models returned an unreadable response")
	}
	ids := make([]string, len(payload.Data))
	for i, m := range payload.Data {
		ids[i] = m.ID
	}
	return ids, nil
}

// authRejectedMessage replaces the provider's own text on a 401/403. Providers echo a masked copy
// of the key there ("Incorrect API key provided: sk-proj-****abcd"), which spec NFR1 keeps out of
// every response, so the text is never passed on.
const authRejectedMessage = "API key rejected by the provider"

// minKeyFragment is the shortest run of the key that is scrubbed from provider text.
const minKeyFragment = 4

var (
	// keyLikeRun matches provider key shapes (sk-…, sk-or-…, sk-ant-…, sk-proj-…), masked or not.
	keyLikeRun = regexp.MustCompile(`(?i)\bsk-[\w*•…-]*`)
	// maskedRun matches any word run holding a mask (two or more of * • …).
	maskedRun = regexp.MustCompile(`[\w-]*[*•…]{2,}[\w-]*`)
)

// apiMessage returns the text for a non-2xx provider response. A 401/403 gets a fixed message;
// otherwise it is the provider's own error text ({"error":{"message":…}}) for OpenAI and
// Anthropic, scrubbed of key fragments. OpenRouter errors carry only the status (spec §7.2).
func apiMessage(name, key string, status int, body []byte) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return authRejectedMessage
	}
	if name == "OpenRouter" {
		return ""
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	// Bound the text first so the scrub stays cheap, then scrub, then cut: a cut after the scrub
	// cannot leave a key fragment of minKeyFragment or more behind.
	msg := scrubKey(truncate(strings.TrimSpace(payload.Error.Message), 4*maxMessageBytes), key)
	return truncate(msg, maxMessageBytes)
}

// scrubKey masks the whole key, key-like runs and every run of msg of at least minKeyFragment
// bytes that is a substring of key.
func scrubKey(msg, key string) string {
	if key != "" {
		msg = strings.ReplaceAll(msg, key, logging.Redacted)
	}
	msg = keyLikeRun.ReplaceAllString(msg, logging.Redacted)
	msg = maskedRun.ReplaceAllString(msg, logging.Redacted)
	if len(key) < minKeyFragment {
		return msg
	}
	var out strings.Builder
	for i := 0; i < len(msg); {
		n := 0
		if i+minKeyFragment <= len(msg) && strings.Contains(key, msg[i:i+minKeyFragment]) {
			n = minKeyFragment
			for i+n < len(msg) && strings.Contains(key, msg[i:i+n+1]) {
				n++
			}
		}
		if n == 0 {
			out.WriteByte(msg[i])
			i++
			continue
		}
		out.WriteString(logging.Redacted)
		i += n
	}
	return out.String()
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
