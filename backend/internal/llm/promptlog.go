package llm

import "context"

// Prompt section trust levels.
const (
	Trusted   = "trusted"
	Untrusted = "untrusted"
)

// PromptSection describes one part of a prompt for the prompt_assembled log line: sizes and
// labels only, never text.
type PromptSection struct {
	// Name is the section name.
	Name string
	// Role is the message role the section belongs to ("system" or "user"). Empty = system for a
	// section named "system", user for any other.
	Role string
	// Source says where the content came from (engine, repo, ...).
	Source string
	// Trust is Trusted or Untrusted.
	Trust string
	// Chars is the section length in UTF-16 code units (see UTF16Len).
	Chars int
	// Items is the number of items in a list-like section (sampled files); nil = not set.
	Items *int
	// Truncated says the section's text was cut (the PR description); nil = not set.
	Truncated *bool
}

type sectionRecord struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Role   string `json:"role"`
	Trust  string `json:"trust"`
	Chars  int    `json:"chars"`
	Tokens int    `json:"tokens"`
	Items  *int   `json:"items,omitempty"`
	// Truncated is set only on sections that report it.
	Truncated *bool `json:"truncated,omitempty"`
}

type chunkRecord struct {
	Index int `json:"index"`
	Total int `json:"total"`
}

// ceilTokens is the 4-characters-per-token estimate.
func ceilTokens(chars int) int { return (chars + 3) / 4 }

// logPrompt writes the content-free prompt_assembled line (spec FR215, section 7.2).
func (c *Client) logPrompt(ctx context.Context, req Request) {
	sections := req.Sections
	if len(sections) == 0 {
		for _, m := range req.Messages {
			sections = append(sections, PromptSection{
				Name: string(m.Role), Source: "engine", Trust: Trusted, Chars: UTF16Len(m.Content),
			})
		}
	}
	records := make([]sectionRecord, len(sections))
	totalChars, totalTokens := 0, 0
	for i, s := range sections {
		role := s.Role
		if role == "" {
			role = "user"
			if s.Name == "system" {
				role = "system"
			}
		}
		tokens := ceilTokens(s.Chars)
		records[i] = sectionRecord{Name: s.Name, Source: s.Source, Role: role, Trust: s.Trust, Chars: s.Chars, Tokens: tokens, Items: s.Items, Truncated: s.Truncated}
		totalChars += s.Chars
		totalTokens += tokens
	}
	attrs := []any{
		"evt", "prompt_assembled",
		"feature", req.Feature,
		"correlationId", req.CorrelationID,
		"provider", string(req.Provider),
		"model", req.Model,
		"sections", records,
		"totalChars", totalChars,
		"totalTokens", totalTokens,
	}
	if req.Chunk != nil {
		attrs = append(attrs, "chunk", chunkRecord{Index: req.Chunk.Index, Total: req.Chunk.Total})
	}
	c.log.InfoContext(ctx, "prompt_assembled", attrs...)
}
