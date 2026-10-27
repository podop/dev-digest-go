// Package app is the Go CI runner's use case (spec 7.8): Run loads the agent manifest and skills
// the export wrote into the repository, fetches the pull request's diff, runs the same review
// engine local reviews and evals run, writes the result artifact, posts the review and returns the
// process exit code. It does no I/O of its own: environment, files, GitHub, the model and the job
// log are injected, so the runner's import closure stays the engine, the LLM layer and a YAML
// parser (no Echo, GORM, database driver, git or secrets-file code).
//
// This file holds the ports; api.go holds the entry point and its inputs.
package app

import (
	"context"
	"errors"

	cidomain "dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/llm/custom"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ErrFileTooLarge is returned by Files.ReadFile when the file is larger than the cap.
var ErrFileTooLarge = errors.New("file too large")

// Env is the process environment (spec 7.8.3). It is the only channel for secrets.
type Env interface {
	// Lookup returns the value of a variable and whether it is set; a set empty value is
	// ("", true), which matters for variables that default only when unset.
	Lookup(key string) (string, bool)
	// Cwd returns the working directory, the base of the default DEVDIGEST_DIR and result path.
	Cwd() string
}

// Files is the file system the runner reads its configuration from and writes the artifact to.
type Files interface {
	// ReadFile returns the content of path; ErrFileTooLarge when it is larger than max bytes.
	ReadFile(path string, max int64) ([]byte, error)
	// ReadDir returns the names of the entries of a directory.
	ReadDir(path string) ([]string, error)
	// WriteFile writes data to path, replacing it.
	WriteFile(path string, data []byte) error
}

// PullRef names the pull request under review.
type PullRef struct {
	Owner, Repo string
	Number      int
}

// InlineComment is an inline comment of a review.
type InlineComment struct {
	Path string
	Line int
	Body string
}

// ReviewPost is the body of a pull request review. Event is APPROVE, REQUEST_CHANGES or COMMENT;
// the use case maps APPROVE to COMMENT before the call.
type ReviewPost struct {
	Body     string
	Event    string
	Comments []InlineComment
}

// APIError is a non-2xx GitHub answer. Its text is the complete job-log message of spec 7.8.5 and
// 7.8.9, "GitHub API error ... (<url>): <status> <body cut to 1,000 bytes, token redacted>".
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// GitHub is the part of the GitHub REST API the runner calls (adapters/ghapi). The token travels
// with every call and never appears in a returned error. Redirects are not followed.
type GitHub interface {
	// FetchDiff returns the unified diff of the pull request (the diff media type), at most 64 MiB;
	// a larger body or a non-2xx answer is an error.
	FetchDiff(ctx context.Context, token string, pr PullRef) (string, error)
	// PostReview posts a pull request review; a non-2xx answer is an *APIError.
	PostReview(ctx context.Context, token string, pr PullRef, post ReviewPost) error
	// PostComment posts an issue comment on the pull request; a non-2xx answer is an *APIError.
	PostComment(ctx context.Context, token string, pr PullRef, body string) error
}

// MRRef names the merge request under review: the project by its numeric id (digits only, the
// API addresses the project by CI_PROJECT_ID) and the merge request iid.
type MRRef struct {
	ProjectID string
	IID       int
}

// DiffRefs are the three commits a positioned draft note is anchored to (GitLab diff_refs).
type DiffRefs struct {
	BaseSHA, StartSHA, HeadSHA string
}

// MergeRequest is the part of a merge request the runner reads. DiffRefs is nil when GitLab has
// not computed them (yet) or answered incompletely.
type MergeRequest struct {
	IID      int
	DiffRefs *DiffRefs
}

// DraftPosition anchors a draft note to a line of the merge request diff (position_type text).
// OldLine is 0 (omitted) unless the new-side line is an unchanged context line.
type DraftPosition struct {
	Refs             DiffRefs
	NewPath, OldPath string
	NewLine, OldLine int
}

// NewDraft is a draft note to create; a nil Position is a note on the whole merge request.
type NewDraft struct {
	Note     string
	Position *DraftPosition
}

// Draft is a draft note (pending, unpublished) of the token user.
type Draft struct {
	ID int
}

// GitLabAPIError is a non-2xx GitLab answer. Its text is the complete job-log message of spec FR28,
// "GitLab API error posting MR review|MR comment (<path>): <status> <body cut to 1,000 bytes, token
// redacted>". Only a 400 on a positioned draft changes the use case's course (FR26).
type GitLabAPIError struct {
	Status  int
	Message string
}

func (e *GitLabAPIError) Error() string { return e.Message }

// GitLab is the part of the GitLab REST API the runner calls in GitLab mode (spec 7.5; the runner
// binary wires adapters/glapi through a thin adapter that maps these types). The token travels with
// every call and never appears in a returned error. Redirects are not followed, writes are not
// retried, and nothing is ever sent as reviewer_state, an approval or a quick action. A non-2xx
// answer is a *GitLabAPIError.
type GitLab interface {
	// MR reads the merge request (diff_refs).
	MR(ctx context.Context, token string, ref MRRef) (MergeRequest, error)
	// ListDrafts lists the token user's pending draft notes on the merge request (first page of 100).
	ListDrafts(ctx context.Context, token string, ref MRRef) ([]Draft, error)
	// CreateDraft creates a draft note (not yet visible to anyone).
	CreateDraft(ctx context.Context, token string, ref MRRef, d NewDraft) (Draft, error)
	// DeleteDraft deletes a pending draft note.
	DeleteDraft(ctx context.Context, token string, ref MRRef, id int) error
	// PublishDraft publishes one draft note.
	PublishDraft(ctx context.Context, token string, ref MRRef, id int) error
	// BulkPublish publishes every pending draft of the token user on the merge request as one review.
	BulkPublish(ctx context.Context, token string, ref MRRef) error
	// CreateNote posts one note on the merge request (post_as pr_comment).
	CreateNote(ctx context.Context, token string, ref MRRef, body string) error
}

// GitDiff computes the unified diff from base to head in the job's checkout (spec FR20) without
// any network request or token. A commit absent from the checkout is an error that satisfies
// interface{ CommitMissing() string } (the missing sha) and whose text is the job-log message of
// FR20; the use case recognises it with errors.As and adds nothing to it.
type GitDiff interface {
	Diff(ctx context.Context, base, head string) (string, error)
}

// LLM builds the engine's LLM port for a model of an openrouter manifest (adapters over the
// OpenRouter client, with the runner's fixed endpoint, retries and timeouts). It is bound to the
// OPENROUTER_API_KEY of the environment; an empty key fails at the first call, not here. Other
// manifest providers go through Providers.
type LLM interface {
	For(model string) reviewerapp.LLM
}

// Header is an extra request header of a custom provider with its value.
type Header struct {
	Name, Value string
}

// CustomEndpoint is everything a custom provider call needs beyond the shared fields of
// ProviderSelection. The use case has already applied the FR3 base URL rules and the FR4 header
// rules; the adapter still applies the outbound policy (no redirects, no proxy, the address guard).
type CustomEndpoint struct {
	// BaseURL is the normalised DEVDIGEST_LLM_BASE_URL (custom.CheckBaseURL).
	BaseURL  string
	Protocol custom.Protocol
	AuthMode custom.AuthMode
	// AuthHeader is the credential header of the header auth mode, empty for bearer.
	AuthHeader string
	// Headers are the extra headers with their values, in manifest order.
	Headers []Header
	// Mode is the structured-output mode (FR33).
	Mode custom.Mode
}

// ProviderSelection names the provider and model of a manifest that is not openrouter, with the
// one credential the use case read for it (A10: the OpenRouter key is never part of it).
type ProviderSelection struct {
	// Provider is "openai", "anthropic" or "custom:<slug>". It is also the name the strict-mode
	// hint uses (SCR4).
	Provider string
	Model    string
	// Key is OPENAI_API_KEY, ANTHROPIC_API_KEY or DEVDIGEST_LLM_API_KEY; empty is allowed here:
	// a built-in fails at its first call, a custom provider then sends no auth header (EC19).
	Key string
	// Price is the manifest price; nil = unpriced. Adapters price reported tokens only (SCR7), for
	// instance with ManifestPricer.
	Price *cidomain.Pricing
	// Custom is set exactly for a custom provider.
	Custom *CustomEndpoint
}

// Secrets are the values the adapter must keep out of every error text and log line: the key and
// the extra header values.
func (s ProviderSelection) Secrets() []string {
	var out []string
	if s.Key != "" {
		out = append(out, s.Key)
	}
	if s.Custom != nil {
		for _, h := range s.Custom.Headers {
			out = append(out, h.Value)
		}
	}
	return out
}

// Providers builds the engine's LLM port for an openai, anthropic or custom manifest (the runner
// binary wires the adapters: the built-in endpoints with their fixed limits, or a custom adapter
// over Custom). It fails only when it cannot build the port; a rejected key shows at the first call.
type Providers interface {
	For(sel ProviderSelection) (reviewerapp.LLM, error)
}

// ManifestPricer prices a model from the manifest price (P5, FR36): tokens_in x promptPerM / 1e6 +
// tokens_out x completionPerM / 1e6. It satisfies llm.Pricer. A nil Pricing prices nothing. The
// caller passes only token counts a provider reported (SCR7).
type ManifestPricer struct {
	Pricing *cidomain.Pricing
}

// Estimate returns the cost of the tokens in USD, or nil when the manifest has no price or a count
// is negative.
func (p ManifestPricer) Estimate(_ string, tokensIn, tokensOut int) *float64 {
	if p.Pricing == nil || tokensIn < 0 || tokensOut < 0 {
		return nil
	}
	cost := float64(tokensIn)*p.Pricing.PromptPerM/1e6 + float64(tokensOut)*p.Pricing.CompletionPerM/1e6
	return &cost
}

// Out is the job log. Lines carry no environment value other than those spec 7.8.9 lists, no
// token or key, and no diff or prompt text.
type Out interface {
	Stdout(line string)
	Stderr(line string)
}

// Engine is the review engine's entry point (reviewer/app.Run); tests script it.
type Engine func(ctx context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error)
