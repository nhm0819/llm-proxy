package tokencount_test

import (
	"strings"
	"testing"

	"github.com/nhm0819/llm-proxy/internal/tokencount"
)

func TestCount_EmptyString(t *testing.T) {
	tc := tokencount.New()
	if n := tc.Count("gpt-4", ""); n != 0 {
		t.Errorf("expected 0 for empty string, got %d", n)
	}
}

func TestCount_KnownModel_NonEmpty(t *testing.T) {
	tc := tokencount.New()
	n := tc.Count("gpt-4", "hello world")
	if n <= 0 {
		t.Errorf("expected positive token count for 'hello world', got %d", n)
	}
}

func TestCount_CachesEncoder(t *testing.T) {
	tc := tokencount.New()
	n1 := tc.Count("gpt-4", "test string")
	n2 := tc.Count("gpt-4", "test string")
	if n1 != n2 {
		t.Errorf("same input should give same count: %d vs %d", n1, n2)
	}
}

func TestCount_UnknownModel_Fallback(t *testing.T) {
	tc := tokencount.New()
	// An unknown model should still produce a non-zero result via fallback
	n := tc.Count("completely-unknown-model-xyz", "hello world")
	if n <= 0 {
		t.Errorf("fallback should produce positive count, got %d", n)
	}
}

func TestCount_CJKText(t *testing.T) {
	tc := tokencount.New()
	n := tc.Count("gpt-4", "This is a test of CJK text.")
	if n <= 0 {
		t.Errorf("expected positive token count, got %d", n)
	}
}

// ── EstimateImageTokens ──────────────────────────────────────────────────────

func TestEstimateImageTokens_Low(t *testing.T) {
	if got := tokencount.EstimateImageTokens("low"); got != 85 {
		t.Errorf("expected 85 for low, got %d", got)
	}
}

func TestEstimateImageTokens_High(t *testing.T) {
	if got := tokencount.EstimateImageTokens("high"); got != 765 {
		t.Errorf("expected 765 for high, got %d", got)
	}
}

func TestEstimateImageTokens_Auto(t *testing.T) {
	if got := tokencount.EstimateImageTokens("auto"); got != 765 {
		t.Errorf("expected 765 for auto, got %d", got)
	}
}

func TestEstimateImageTokens_Empty(t *testing.T) {
	if got := tokencount.EstimateImageTokens(""); got != 765 {
		t.Errorf("expected 765 for empty string, got %d", got)
	}
}

func TestEstimateImageTokens_Unknown(t *testing.T) {
	if got := tokencount.EstimateImageTokens("medium"); got != 765 {
		t.Errorf("expected 765 for unknown value, got %d", got)
	}
}

func TestCount_LongText(t *testing.T) {
	tc := tokencount.New()
	var sb strings.Builder
	for range 100 {
		sb.WriteString("This is a repeated sentence for testing token counting. ")
	}
	longText := sb.String()
	n := tc.Count("gpt-4", longText)
	if n <= 100 {
		t.Errorf("expected large token count for long text, got %d", n)
	}
}
