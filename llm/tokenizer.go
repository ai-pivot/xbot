package llm

import (
	"cmp"
	"slices"
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// modelToEncoding maps model names to their tokenizer model constants
var modelToEncoding = map[string]tokenizer.Model{
	// GPT-4 series
	"gpt-4":                  tokenizer.GPT4,
	"gpt-4-0314":             tokenizer.GPT4,
	"gpt-4-0613":             tokenizer.GPT4,
	"gpt-4-32k":              tokenizer.GPT4, // 32k uses same encoding as GPT4
	"gpt-4-32k-0314":         tokenizer.GPT4,
	"gpt-4-32k-0613":         tokenizer.GPT4,
	"gpt-4-turbo":            tokenizer.GPT4,
	"gpt-4-turbo-2024-04-09": tokenizer.GPT4,
	"gpt-4o":                 tokenizer.GPT4o,
	"gpt-4o-2024-05-13":      tokenizer.GPT4o,
	"gpt-4o-mini":            tokenizer.GPT4o,
	"gpt-4o-mini-2024-07-18": tokenizer.GPT4o,

	// GPT-3.5 series
	"gpt-3.5-turbo":      tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-0301": tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-0613": tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-1106": tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-0125": tokenizer.GPT35Turbo,

	// Claude series (uses cl100k_base as approximate tokenizer)
	// Note: Claude's actual tokenizer differs from cl100k_base with 10-20% deviation.
	// We use cl100k_base as an approximation since Claude's native tokenizer is not
	// publicly available. Token counts should be treated as estimates, not exact values.
	"claude-3-opus":              tokenizer.GPT4,
	"claude-3-sonnet":            tokenizer.GPT4,
	"claude-3-haiku":             tokenizer.GPT4,
	"claude-3-5-sonnet":          tokenizer.GPT4,
	"claude-3-5-sonnet-20240620": tokenizer.GPT4,
	"claude-3-5-sonnet-20241022": tokenizer.GPT4,
	"claude-3-5-haiku":           tokenizer.GPT4,
	"claude-2":                   tokenizer.GPT4,
	"claude-2.1":                 tokenizer.GPT4,
	"claude-instant":             tokenizer.GPT4,
	"claude-sonnet-4-20250514":   tokenizer.GPT4,
	"claude-opus-4-20250115":     tokenizer.GPT4,
	"claude-3-7-sonnet-20250219": tokenizer.GPT4,

	// MiniMax series (uses cl100k_base)
	"abab6.5s-chat": tokenizer.GPT35Turbo,
	"abab6.5g-chat": tokenizer.GPT35Turbo,
	"abab6s-chat":   tokenizer.GPT35Turbo,

	// DeepSeek
	"deepseek-chat":  tokenizer.GPT4,
	"deepseek-coder": tokenizer.GPT4,

	// Other models - default to GPT-4 encoding
	"default": tokenizer.GPT4,
}

// getSortedPrefixes returns sorted model prefixes for prefix matching.
// Sorted by length descending (longest first) to avoid mis匹配.
var getSortedPrefixes = sync.OnceValue(func() []string {
	var prefixes []string
	for k := range modelToEncoding {
		if k != "default" {
			prefixes = append(prefixes, k)
		}
	}
	slices.SortFunc(prefixes, func(a, b string) int {
		return cmp.Compare(len(b), len(a)) // longest first
	})
	return prefixes
})

// getEncodingForModel returns the tokenizer model for a given model name
func getEncodingForModel(model string) tokenizer.Model {
	model = strings.ToLower(model)

	// Direct match
	if encoding, ok := modelToEncoding[model]; ok {
		return encoding
	}

	// Prefix match for models like "gpt-4o-xxx" -> "gpt-4o"
	// Use cached sorted prefixes (sorted by length descending)
	prefixes := getSortedPrefixes()

	for _, prefix := range prefixes {
		if strings.HasPrefix(model, prefix) {
			return modelToEncoding[prefix]
		}
	}

	return tokenizer.GPT4 // Default fallback
}

// encoderCache caches tokenizer encoders to avoid repeated initialization
var encoderCache sync.Map // map[tokenizer.Model]tokenizer.Codec

// getEncoder returns a cached encoder for the given model, or creates a new one
func getEncoder(encodingModel tokenizer.Model) (tokenizer.Codec, error) {
	if enc, ok := encoderCache.Load(encodingModel); ok {
		return enc.(tokenizer.Codec), nil
	}
	enc, err := tokenizer.ForModel(encodingModel)
	if err != nil {
		return nil, err
	}
	encoderCache.Store(encodingModel, enc)
	return enc, nil
}

// CountTokens counts the number of tokens in the given text for the specified model.
// Returns the token count and any error.
func CountTokens(text string, model string) (int, error) {
	encodingModel := getEncodingForModel(model)

	// Get the encoder (with caching)
	enc, err := getEncoder(encodingModel)
	if err != nil {
		// Fallback to GPT-4 encoder
		enc, err = getEncoder(tokenizer.GPT4)
		if err != nil {
			return 0, err
		}
	}

	// Encode and count
	ids, _, err := enc.Encode(text)
	if err != nil {
		return 0, err
	}

	return len(ids), nil
}
