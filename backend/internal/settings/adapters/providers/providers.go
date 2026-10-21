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
	"strings"
	"time"

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
	if c.http == nil {
		c.http = &http.Client{}
	}
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

// CountModels lists the provider's models with key and returns how many count: for OpenAI the
// ids starting with gpt or containing o1 or o3, for Anthropic the first page, for OpenRouter
// all listed models.
func (c *Client) CountModels(ctx context.Context, provider app.Provider, key string) (int, error) {
	switch provider {
	case app.OpenAI:
		ids, err := c.withRetry(ctx, func(ctx context.Context) ([]string, error) {
			return c.listIDs(ctx, "OpenAI", c.openAI+"/models", key, map[string]string{"Authorization": "Bearer " + key}, c.timeout)
		})
		if err != nil {
			return 0, err
		}
		n := 0
		for _, id := range ids {
			if strings.HasPrefix(id, "gpt") || strings.Contains(id, "o1") || strings.Contains(id, "o3") {
				n++
			}
		}
		return n, nil
	case app.Anthropic:
		ids, err := c.withRetry(ctx, func(ctx context.Context) ([]string, error) {
			return c.listIDs(ctx, "Anthropic", c.anthropic+"/models", key,
				map[string]string{"x-api-key": key, "anthropic-version": anthropicVersion}, c.timeout)
		})
		return len(ids), err
	case app.OpenRouter:
		ids, err := c.listIDs(ctx, "OpenRouter", c.openRouter+"/models", key, map[string]string{"Authorization": "Bearer " + key}, c.openRouterTimeout)
		return len(ids), err
	default:
		return 0, fmt.Errorf("provider %q has no model list", provider)
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
		return nil, &statusError{provider: name, status: resp.StatusCode, message: apiMessage(name, key, body)}
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

// apiMessage extracts the provider's error text ({"error":{"message":…}}) for OpenAI and
// Anthropic. OpenRouter errors carry only the status (spec §7.2).
func apiMessage(name, key string, body []byte) string {
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
	msg := strings.TrimSpace(payload.Error.Message)
	if key != "" {
		// Scrub before truncating, so a cut can never leave a key prefix behind.
		msg = strings.ReplaceAll(msg, key, logging.Redacted)
	}
	if len(msg) > maxMessageBytes {
		msg = msg[:maxMessageBytes]
	}
	return msg
}
