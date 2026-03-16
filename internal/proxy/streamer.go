package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// StreamCallbacks groups callbacks for SSE streaming.
type StreamCallbacks struct {
	// OnRawLine is called for every raw text line (including the trailing \n).
	OnRawLine func(line string)
	// OnEvent is called once per complete SSE event with its data payload.
	OnEvent func(eventData string)
}

// StreamSSE reads an SSE body line-by-line, calling cb.OnRawLine for each
// line and cb.OnEvent once per complete event (blank-line delimiter).
// It returns any scanner error; connection drops are handled by the caller.
func StreamSSE(body io.Reader, cb StreamCallbacks) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 5<<20)

	var dataLines []string
	for sc.Scan() {
		line := sc.Text()
		if cb.OnRawLine != nil {
			cb.OnRawLine(line + "\n")
		}

		if line == "" {
			if len(dataLines) > 0 {
				cb.OnEvent(strings.Join(dataLines, "\n"))
				dataLines = dataLines[:0]
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	// Flush any unterminated event
	if len(dataLines) > 0 {
		cb.OnEvent(strings.Join(dataLines, "\n"))
	}
	return sc.Err()
}

// StreamEventParser accumulates token counts and output text from SSE events
// for a given API kind.
type StreamEventParser struct {
	Kind Kind

	// Outputs (populated as events are parsed)
	ActualTotalTokens int
	OutputCharCount   int

	hashBuilder  interface{ WriteString(string) }
	excerptBuf   *strings.Builder
	excerptLimit int
	piiCallback  func(delta string) // called with each output fragment
}

// NewStreamEventParser creates a parser for the given kind.
// hashBuilder receives output text fragments for hashing.
// excerptBuf is written up to excerptLimit bytes of output text.
// piiCallback is invoked for every text fragment so the caller can detect PII.
func NewStreamEventParser(
	kind Kind,
	hashBuilder interface{ WriteString(string) },
	excerptBuf *strings.Builder,
	excerptLimit int,
	piiCallback func(string),
) *StreamEventParser {
	return &StreamEventParser{
		Kind:         kind,
		hashBuilder:  hashBuilder,
		excerptBuf:   excerptBuf,
		excerptLimit: excerptLimit,
		piiCallback:  piiCallback,
	}
}

// Parse processes one SSE event data string and updates the parser state.
func (p *StreamEventParser) Parse(eventData string) {
	if eventData == "[DONE]" || eventData == "" {
		return
	}
	var obj map[string]any
	if json.Unmarshal([]byte(eventData), &obj) != nil {
		return
	}

	switch p.Kind {
	case KindResponses:
		p.parseResponses(obj)
	case KindChat, KindCompletions:
		p.parseChatCompletions(obj)
	}
}

func (p *StreamEventParser) parseResponses(obj map[string]any) {
	typ := getString(obj, "type")
	switch typ {
	case "response.output_text.delta":
		delta := getString(obj, "delta")
		p.recordFragment(delta)

	case "response.completed":
		if respObj, ok := obj["response"].(map[string]any); ok {
			if u, ok := respObj["usage"].(map[string]any); ok {
				p.ActualTotalTokens = getInt(u, "total_tokens")
			}
			// If no incremental deltas were captured, hash the full output text
			if p.OutputCharCount == 0 {
				full := CollectResponsesObjectText(respObj)
				p.recordFragment(full)
			}
		}
	}
}

func (p *StreamEventParser) parseChatCompletions(obj map[string]any) {
	// Usage chunk (often the final chunk with empty choices)
	if u, ok := obj["usage"].(map[string]any); ok {
		if t := getInt(u, "total_tokens"); t > 0 {
			p.ActualTotalTokens = t
		}
	}

	choices, ok := obj["choices"].([]any)
	if !ok {
		return
	}
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		// Chat: delta.content
		if d, ok := cm["delta"].(map[string]any); ok {
			if s, ok := d["content"].(string); ok && s != "" {
				p.recordFragment(s)
			}
		}
		// Legacy completions: choice.text
		if s, ok := cm["text"].(string); ok && s != "" {
			p.recordFragment(s)
		}
	}
}

func (p *StreamEventParser) recordFragment(s string) {
	if s == "" {
		return
	}
	p.hashBuilder.WriteString(s)
	p.OutputCharCount += len(s)

	if p.excerptBuf != nil && p.excerptBuf.Len() < p.excerptLimit {
		remaining := p.excerptLimit - p.excerptBuf.Len()
		if len(s) > remaining {
			s = s[:remaining]
		}
		p.excerptBuf.WriteString(s)
	}

	if p.piiCallback != nil {
		p.piiCallback(s)
	}
}
