// Package proxy contains helpers for classifying API paths, extracting
// structured fields from request/response bodies, and computing token counts.
package proxy

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/nhm0819/llm-proxy/internal/config"
)

// Kind classifies the API endpoint type.
type Kind string

const (
	KindChat        Kind = "chat"
	KindCompletions Kind = "completions"
	KindResponses   Kind = "responses"
	KindOther       Kind = "other"
)

// ClassifyPath maps a URL path to a Kind.
func ClassifyPath(path string) Kind {
	switch path {
	case "/v1/responses":
		return KindResponses
	case "/v1/chat/completions":
		return KindChat
	case "/v1/completions":
		return KindCompletions
	default:
		return KindOther
	}
}

// IsGenerative returns true for endpoint kinds that produce token output.
func IsGenerative(k Kind) bool {
	return k == KindResponses || k == KindChat || k == KindCompletions
}

// ExtractModel returns the "model" field from a parsed JSON body.
func ExtractModel(body map[string]any) string {
	return getString(body, "model")
}

// ExtractUserID returns the user identifier, preferring the X-User-ID header
// then the body "user" field, then "safety_identifier".
func ExtractUserID(r *http.Request, body map[string]any) string {
	const headerUserID = "X-User-ID"
	if v := r.Header.Get(headerUserID); v != "" {
		return v
	}
	if s := getString(body, "user"); s != "" {
		return s
	}
	return getString(body, "safety_identifier")
}

// ExtractMaxOutputTokens returns the caller-specified or default max output
// token count for the given endpoint kind.
func ExtractMaxOutputTokens(k Kind, body map[string]any, cfg config.Config) int {
	if body == nil {
		return 0
	}
	switch k {
	case KindResponses:
		if v := getInt(body, "max_output_tokens"); v > 0 {
			return v
		}
		return cfg.DefaultMaxOutputTokensResponses
	case KindChat:
		if v := getInt(body, "max_completion_tokens"); v > 0 {
			return v
		}
		if v := getInt(body, "max_tokens"); v > 0 {
			return v
		}
		return cfg.DefaultMaxCompletionTokensChat
	case KindCompletions:
		if v := getInt(body, "max_tokens"); v > 0 {
			return v
		}
		return cfg.DefaultMaxCompletionTokensChat
	default:
		return 0
	}
}

// CollectRequestText concatenates all text content from a request body for
// token estimation and PII scanning.
func CollectRequestText(k Kind, body map[string]any) string {
	if body == nil {
		return ""
	}
	var parts []string
	switch k {
	case KindChat:
		parts = extractChatMessages(body)
	case KindCompletions:
		parts = extractCompletionPrompt(body)
	case KindResponses:
		parts = extractResponsesInput(body)
	}
	return strings.Join(parts, "\n")
}

// CollectResponseText extracts assistant output text from a response body.
func CollectResponseText(k Kind, body map[string]any) string {
	if body == nil {
		return ""
	}
	switch k {
	case KindChat:
		return strings.Join(extractChatResponseChoices(body), "\n")
	case KindCompletions:
		return strings.Join(extractCompletionChoices(body), "\n")
	case KindResponses:
		return CollectResponsesObjectText(body)
	default:
		return ""
	}
}

// CollectResponsesObjectText extracts text from the Responses API response object.
// Exported so it can be reused from the SSE streaming handler.
func CollectResponsesObjectText(obj map[string]any) string {
	var parts []string
	if ot, ok := obj["output_text"].(string); ok && ot != "" {
		parts = append(parts, ot)
	}
	if out, ok := obj["output"].([]any); ok {
		for _, item := range out {
			im, _ := item.(map[string]any)
			if im == nil {
				continue
			}
			if c, ok := im["content"].([]any); ok {
				for _, pv := range c {
					pm, _ := pv.(map[string]any)
					if pm == nil {
						continue
					}
					if txt, ok := pm["text"].(string); ok && txt != "" {
						parts = append(parts, txt)
					}
					if rf, ok := pm["refusal"].(string); ok && rf != "" {
						parts = append(parts, rf)
					}
				}
			}
			if a, ok := im["arguments"].(string); ok && a != "" {
				parts = append(parts, a)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// ExtractTotalTokens reads usage.total_tokens from a response body.
// Returns 0 if not present or the kind is not generative.
func ExtractTotalTokens(body map[string]any) int {
	if body == nil {
		return 0
	}
	u, ok := body["usage"].(map[string]any)
	if !ok {
		return 0
	}
	return getInt(u, "total_tokens")
}

// ---------- private extraction helpers ----------

func extractChatMessages(body map[string]any) []string {
	var parts []string
	msgs, ok := body["messages"].([]any)
	if !ok {
		return parts
	}
	for _, mv := range msgs {
		mm, _ := mv.(map[string]any)
		if mm == nil {
			continue
		}
		switch c := mm["content"].(type) {
		case string:
			parts = append(parts, c)
		case []any:
			for _, pv := range c {
				pm, _ := pv.(map[string]any)
				if pm == nil {
					continue
				}
				for _, k := range []string{"text", "input_text", "refusal"} {
					if t, ok := pm[k].(string); ok && t != "" {
						parts = append(parts, t)
					}
				}
			}
		}
	}
	return parts
}

func extractCompletionPrompt(body map[string]any) []string {
	var parts []string
	switch t := body["prompt"].(type) {
	case string:
		parts = append(parts, t)
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
	}
	return parts
}

func extractResponsesInput(body map[string]any) []string {
	var parts []string
	if ins := getString(body, "instructions"); ins != "" {
		parts = append(parts, ins)
	}
	switch t := body["input"].(type) {
	case string:
		parts = append(parts, t)
	case []any:
		for _, item := range t {
			im, _ := item.(map[string]any)
			if im == nil {
				continue
			}
			if cs, ok := im["content"].(string); ok && cs != "" {
				parts = append(parts, cs)
			}
			if ca, ok := im["content"].([]any); ok {
				for _, pv := range ca {
					pm, _ := pv.(map[string]any)
					if pm == nil {
						continue
					}
					for _, k := range []string{"text", "refusal"} {
						if txt, ok := pm[k].(string); ok && txt != "" {
							parts = append(parts, txt)
						}
					}
				}
			}
			if a, ok := im["arguments"].(string); ok && a != "" {
				parts = append(parts, a)
			}
		}
	}
	return parts
}

func extractChatResponseChoices(body map[string]any) []string {
	var parts []string
	choices, ok := body["choices"].([]any)
	if !ok {
		return parts
	}
	for _, cv := range choices {
		cm, _ := cv.(map[string]any)
		if cm == nil {
			continue
		}
		if msg, ok := cm["message"].(map[string]any); ok {
			switch c := msg["content"].(type) {
			case string:
				parts = append(parts, c)
			case []any:
				for _, pv := range c {
					pm, _ := pv.(map[string]any)
					if pm == nil {
						continue
					}
					for _, k := range []string{"text", "refusal"} {
						if t, ok := pm[k].(string); ok && t != "" {
							parts = append(parts, t)
						}
					}
				}
			}
		}
		if t, ok := cm["text"].(string); ok && t != "" {
			parts = append(parts, t)
		}
	}
	return parts
}

func extractCompletionChoices(body map[string]any) []string {
	var parts []string
	choices, ok := body["choices"].([]any)
	if !ok {
		return parts
	}
	for _, cv := range choices {
		cm, _ := cv.(map[string]any)
		if cm == nil {
			continue
		}
		if t, ok := cm["text"].(string); ok && t != "" {
			parts = append(parts, t)
		}
	}
	return parts
}

// ---------- JSON field helpers ----------

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	default:
		return 0
	}
}

func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return false
	}
}
