package proxy_test

import (
	"strings"
	"testing"

	"github.com/nhm0819/llm-proxy/internal/proxy"
)

// ── StreamSSE ─────────────────────────────────────────────────────────────────

func TestStreamSSE_SingleEvent(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"

	var events []string
	_ = proxy.StreamSSE(strings.NewReader(body), proxy.StreamCallbacks{
		OnEvent: func(data string) { events = append(events, data) },
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if !contains(events[0], "hello") {
		t.Errorf("unexpected event data: %q", events[0])
	}
}

func TestStreamSSE_MultipleEvents(t *testing.T) {
	body := "data: first\n\ndata: second\n\ndata: [DONE]\n\n"

	var events []string
	_ = proxy.StreamSSE(strings.NewReader(body), proxy.StreamCallbacks{
		OnEvent: func(data string) { events = append(events, data) },
	})
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %v", len(events), events)
	}
	if events[0] != "first" || events[1] != "second" || events[2] != "[DONE]" {
		t.Errorf("unexpected events: %v", events)
	}
}

func TestStreamSSE_RawLinePassthrough(t *testing.T) {
	body := "data: hello\n\n"
	var lines []string
	_ = proxy.StreamSSE(strings.NewReader(body), proxy.StreamCallbacks{
		OnRawLine: func(l string) { lines = append(lines, l) },
		OnEvent:   func(_ string) {},
	})
	if len(lines) == 0 {
		t.Fatal("expected raw lines to be called")
	}
	// Each line should end with \n
	for _, l := range lines {
		if !strings.HasSuffix(l, "\n") {
			t.Errorf("raw line missing trailing newline: %q", l)
		}
	}
}

func TestStreamSSE_EmptyBody(t *testing.T) {
	var events []string
	err := proxy.StreamSSE(strings.NewReader(""), proxy.StreamCallbacks{
		OnEvent: func(data string) { events = append(events, data) },
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no events for empty body, got %d", len(events))
	}
}

// ── StreamEventParser ─────────────────────────────────────────────────────────

func TestStreamEventParser_Chat(t *testing.T) {
	hb := &testHashBuilder{}
	var excerptBuf strings.Builder
	var piiFragments []string

	parser := proxy.NewStreamEventParser(
		proxy.KindChat, hb, &excerptBuf, 4096,
		func(delta string) { piiFragments = append(piiFragments, delta) },
	)

	parser.Parse(`{"choices":[{"delta":{"content":"hello "}}]}`)
	parser.Parse(`{"choices":[{"delta":{"content":"world"}}]}`)
	parser.Parse(`{"usage":{"total_tokens":42}}`)
	parser.Parse("[DONE]")

	if hb.written != "hello world" {
		t.Errorf("hash builder got %q, want %q", hb.written, "hello world")
	}
	if parser.ActualTotalTokens != 42 {
		t.Errorf("expected 42 total tokens, got %d", parser.ActualTotalTokens)
	}
	if excerptBuf.String() != "hello world" {
		t.Errorf("excerpt buf = %q", excerptBuf.String())
	}
	if len(piiFragments) != 2 {
		t.Errorf("expected 2 pii callbacks, got %d", len(piiFragments))
	}
}

func TestStreamEventParser_Responses(t *testing.T) {
	hb := &testHashBuilder{}
	var excerptBuf strings.Builder

	parser := proxy.NewStreamEventParser(proxy.KindResponses, hb, &excerptBuf, 4096, nil)

	parser.Parse(`{"type":"response.output_text.delta","delta":"foo "}`)
	parser.Parse(`{"type":"response.output_text.delta","delta":"bar"}`)
	parser.Parse(`{"type":"response.completed","response":{"usage":{"total_tokens":99}}}`)

	if hb.written != "foo bar" {
		t.Errorf("hash builder got %q, want %q", hb.written, "foo bar")
	}
	if parser.ActualTotalTokens != 99 {
		t.Errorf("expected 99 total tokens, got %d", parser.ActualTotalTokens)
	}
}

func TestStreamEventParser_OutputCharCount(t *testing.T) {
	hb := &testHashBuilder{}
	var buf strings.Builder
	parser := proxy.NewStreamEventParser(proxy.KindChat, hb, &buf, 4096, nil)
	parser.Parse(`{"choices":[{"delta":{"content":"abc"}}]}`)
	if parser.OutputCharCount != 3 {
		t.Errorf("expected OutputCharCount=3, got %d", parser.OutputCharCount)
	}
}

func TestStreamEventParser_ExcerptTruncation(t *testing.T) {
	hb := &testHashBuilder{}
	var buf strings.Builder
	limit := 10
	parser := proxy.NewStreamEventParser(proxy.KindChat, hb, &buf, limit, nil)

	// Send 20 chars in two events
	parser.Parse(`{"choices":[{"delta":{"content":"0123456789"}}]}`)
	parser.Parse(`{"choices":[{"delta":{"content":"ABCDEFGHIJ"}}]}`)

	if buf.Len() > limit {
		t.Errorf("excerpt buf exceeded limit %d: got %d chars", limit, buf.Len())
	}
}

// ── testHashBuilder stub ─────────────────────────────────────────────────────

type testHashBuilder struct{ written string }

func (h *testHashBuilder) WriteString(s string) { h.written += s }
