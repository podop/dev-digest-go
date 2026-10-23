// Package keynames holds the names of the provider API keys and the GitHub token, as used in the
// secrets file and as environment variables. It is a leaf: it imports nothing, so a binary that
// reads its keys from the environment only (the CI runner) can name them without linking the
// secrets-file store.
package keynames

// Secret names.
const (
	OpenAIKey     = "OPENAI_API_KEY"
	AnthropicKey  = "ANTHROPIC_API_KEY"
	OpenRouterKey = "OPENROUTER_API_KEY"
	GitHubToken   = "GITHUB_TOKEN"
)
