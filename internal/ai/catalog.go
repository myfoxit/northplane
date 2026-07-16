package ai

// Provider catalog: the built-in provider types a connection can be
// created for, with curated model lists (July 2026) used as fallback
// when the provider's live model listing is unreachable. Everything
// speaks one of two wire dialects — Anthropic Messages or OpenAI
// Chat Completions (which Google, xAI, Mistral, DeepSeek, Groq,
// OpenRouter and Ollama all serve natively or via compat endpoints).

// ProviderKind selects the wire dialect.
type ProviderKind string

const (
	KindAnthropic ProviderKind = "anthropic" // native /v1/messages SSE
	KindOpenAI    ProviderKind = "openai"    // chat/completions SSE dialect
)

// ProviderType describes one connectable provider family.
type ProviderType struct {
	ID       string       `json:"id"`
	Label    string       `json:"label"`
	Kind     ProviderKind `json:"-"`
	Endpoint string       `json:"endpoint"` // default base URL ('' = user must supply)
	// NeedsKey: false for local endpoints (Ollama).
	NeedsKey bool `json:"needsKey"`
	// KeyURL points users to where API keys are created.
	KeyURL string `json:"keyUrl,omitempty"`
	// Models is the curated fallback list, first entry = suggested default.
	Models []ModelInfo `json:"models"`
	// Quirk flags for the OpenAI dialect (see stream_openai.go).
	quirks openAIQuirks
}

// openAIQuirks captures per-provider deviations from vanilla
// chat/completions.
type openAIQuirks struct {
	// reasoningEffortParam: send effort as flat "reasoning_effort".
	reasoningEffortParam bool
	// maxCompletionTokens: use "max_completion_tokens" instead of the
	// legacy "max_tokens" (OpenAI reasoning models reject the latter).
	maxCompletionTokens bool
	// echoReasoningContent: DeepSeek — assistant turns in tool loops must
	// round-trip "reasoning_content" or the API 400s.
	echoReasoningContent bool
	// echoReasoningDetails: OpenRouter — round-trip "reasoning_details"
	// unmodified so upstream thinking blocks survive tool loops.
	echoReasoningDetails bool
	// noSamplingWithTools: avoid temperature/top_p entirely (reasoning
	// endpoints reject them).
	openRouterHeaders bool // send HTTP-Referer/X-Title attribution
}

// providerTypes is the ordered catalog shown in the UI.
var providerTypes = []ProviderType{
	{
		ID: "anthropic", Label: "Anthropic Claude", Kind: KindAnthropic,
		Endpoint: "https://api.anthropic.com", NeedsKey: true,
		KeyURL: "https://console.anthropic.com/settings/keys",
		Models: []ModelInfo{
			{ID: "claude-opus-4-8", Label: "Claude Opus 4.8", Curated: true},
			{ID: "claude-fable-5", Label: "Claude Fable 5", Curated: true},
			{ID: "claude-sonnet-5", Label: "Claude Sonnet 5", Curated: true},
			{ID: "claude-haiku-4-5", Label: "Claude Haiku 4.5", Curated: true},
		},
	},
	{
		ID: "openai", Label: "OpenAI", Kind: KindOpenAI,
		Endpoint: "https://api.openai.com/v1", NeedsKey: true,
		KeyURL: "https://platform.openai.com/api-keys",
		Models: []ModelInfo{
			{ID: "gpt-5.6", Label: "GPT-5.6 (Sol)", Curated: true},
			{ID: "gpt-5.6-terra", Label: "GPT-5.6 Terra", Curated: true},
			{ID: "gpt-5.6-luna", Label: "GPT-5.6 Luna", Curated: true},
			{ID: "gpt-5.2", Label: "GPT-5.2", Curated: true},
		},
		quirks: openAIQuirks{reasoningEffortParam: true, maxCompletionTokens: true},
	},
	{
		ID: "google", Label: "Google Gemini", Kind: KindOpenAI,
		// Google's supported OpenAI-compatible surface; reasoning_effort
		// maps onto Gemini thinking levels server-side.
		Endpoint: "https://generativelanguage.googleapis.com/v1beta/openai", NeedsKey: true,
		KeyURL: "https://aistudio.google.com/apikey",
		Models: []ModelInfo{
			{ID: "gemini-3.5-flash", Label: "Gemini 3.5 Flash", Curated: true},
			{ID: "gemini-3.1-pro-preview", Label: "Gemini 3.1 Pro (Preview)", Curated: true},
			{ID: "gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash-Lite", Curated: true},
		},
		quirks: openAIQuirks{reasoningEffortParam: true, maxCompletionTokens: true},
	},
	{
		ID: "xai", Label: "xAI Grok", Kind: KindOpenAI,
		Endpoint: "https://api.x.ai/v1", NeedsKey: true,
		KeyURL: "https://console.x.ai",
		Models: []ModelInfo{
			{ID: "grok-4.5", Label: "Grok 4.5", Curated: true},
			{ID: "grok-4.3", Label: "Grok 4.3", Curated: true},
		},
		quirks: openAIQuirks{reasoningEffortParam: true, maxCompletionTokens: true},
	},
	{
		ID: "mistral", Label: "Mistral", Kind: KindOpenAI,
		Endpoint: "https://api.mistral.ai/v1", NeedsKey: true,
		KeyURL: "https://console.mistral.ai/api-keys",
		Models: []ModelInfo{
			{ID: "mistral-large-latest", Label: "Mistral Large", Curated: true},
			{ID: "mistral-medium-latest", Label: "Mistral Medium", Curated: true},
			{ID: "mistral-small-latest", Label: "Mistral Small", Curated: true},
		},
	},
	{
		ID: "deepseek", Label: "DeepSeek", Kind: KindOpenAI,
		Endpoint: "https://api.deepseek.com/v1", NeedsKey: true,
		KeyURL: "https://platform.deepseek.com/api_keys",
		Models: []ModelInfo{
			{ID: "deepseek-v4-pro", Label: "DeepSeek V4 Pro", Curated: true},
			{ID: "deepseek-v4-flash", Label: "DeepSeek V4 Flash", Curated: true},
		},
		quirks: openAIQuirks{reasoningEffortParam: true, echoReasoningContent: true},
	},
	{
		ID: "groq", Label: "Groq", Kind: KindOpenAI,
		Endpoint: "https://api.groq.com/openai/v1", NeedsKey: true,
		KeyURL: "https://console.groq.com/keys",
		Models: []ModelInfo{
			{ID: "openai/gpt-oss-120b", Label: "GPT-OSS 120B", Curated: true},
			{ID: "llama-3.3-70b-versatile", Label: "Llama 3.3 70B", Curated: true},
			{ID: "llama-3.1-8b-instant", Label: "Llama 3.1 8B", Curated: true},
		},
	},
	{
		ID: "openrouter", Label: "OpenRouter", Kind: KindOpenAI,
		Endpoint: "https://openrouter.ai/api/v1", NeedsKey: true,
		KeyURL: "https://openrouter.ai/settings/keys",
		Models: []ModelInfo{
			{ID: "openrouter/auto", Label: "Auto (best available)", Curated: true},
			{ID: "anthropic/claude-sonnet-5", Label: "Claude Sonnet 5", Curated: true},
			{ID: "openai/gpt-5.6-luna", Label: "GPT-5.6 Luna", Curated: true},
		},
		quirks: openAIQuirks{echoReasoningDetails: true, openRouterHeaders: true},
	},
	{
		ID: "ollama", Label: "Ollama (lokal)", Kind: KindOpenAI,
		Endpoint: "http://localhost:11434/v1", NeedsKey: false,
		Models: nil, // purely dynamic via /v1/models
	},
	{
		ID: "openai-compat", Label: "OpenAI-kompatibel (eigener Endpoint)", Kind: KindOpenAI,
		Endpoint: "", NeedsKey: false,
		Models: nil,
	},
}

// ProviderTypeByID looks a type up ("" for unknown).
func ProviderTypeByID(id string) *ProviderType {
	for i := range providerTypes {
		if providerTypes[i].ID == id {
			return &providerTypes[i]
		}
	}
	return nil
}

// ProviderTypes returns the catalog for the API layer.
func ProviderTypes() []ProviderType { return providerTypes }
