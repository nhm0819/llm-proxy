// Package tokencount estimates token counts for LLM model inputs.
// It uses tiktoken for OpenAI-compatible models and falls back to a
// rune-based heuristic for unknown encodings.
package tokencount

import (
	"math"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// Counter counts tokens in a text string for a given model.
type Counter interface {
	Count(model, text string) int
}

// TiktokenCounter is a thread-safe token counter backed by tiktoken.
// Encoder instances are cached after first construction.
type TiktokenCounter struct {
	mu    sync.Mutex
	cache map[string]*tiktoken.Tiktoken
}

// New returns an initialised TiktokenCounter.
func New() *TiktokenCounter {
	return &TiktokenCounter{cache: make(map[string]*tiktoken.Tiktoken)}
}

// Count returns the number of tokens in text for the given model name.
// Returns 0 for empty strings.  Falls back to a rough heuristic
// (ceil(runes/2)) when the model encoding is not recognised.
func (tc *TiktokenCounter) Count(model, text string) int {
	if text == "" {
		return 0
	}
	enc := tc.encodingFor(model)
	if enc == nil {
		// ~2 chars/token as a safe overestimate for CJK + Latin mix
		return int(math.Ceil(float64(len([]rune(text))) / 2.0))
	}
	return len(enc.Encode(text, nil, nil))
}

// EstimateImageTokens returns a conservative token estimate for a single image
// based on the detail level.  Values align with OpenAI's vision pricing:
//   - "low"  → 85 tokens (fixed-size thumbnail)
//   - "high" → 765 tokens (multiple tiles)
//   - other  → 765 tokens (conservative default for "auto" or unspecified)
func EstimateImageTokens(detail string) int {
	if detail == "low" {
		return 85
	}
	return 765
}

func (tc *TiktokenCounter) encodingFor(model string) *tiktoken.Tiktoken {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if enc, ok := tc.cache[model]; ok {
		return enc // may be nil (sentinel for "unknown")
	}

	enc, err := tiktoken.EncodingForModel(model)
	if err != nil {
		// Try the widely-compatible base encoding as fallback
		enc2, err2 := tiktoken.GetEncoding("cl100k_base")
		if err2 != nil {
			tc.cache[model] = nil
			return nil
		}
		tc.cache[model] = enc2
		return enc2
	}
	tc.cache[model] = enc
	return enc
}
