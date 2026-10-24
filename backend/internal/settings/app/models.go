package app

// ModelList is what a provider's model-list call returned.
type ModelList struct {
	// IDs is every model id the provider listed, in response order.
	IDs []string
	// Count is how many of them count as usable chat models: for OpenAI the ids starting with
	// gpt or containing o1 or o3, for Anthropic and OpenRouter every listed id.
	Count int
}
