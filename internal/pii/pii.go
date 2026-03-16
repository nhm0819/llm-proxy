// Package pii provides a text scanner that detects and redacts
// Personally Identifiable Information (PII) patterns.
package pii

import (
	"regexp"
	"strconv"
	"strings"
)

// Scanner detects and redacts PII from text strings.
type Scanner struct {
	patterns []pattern
}

type pattern struct {
	typ string
	re  *regexp.Regexp
}

// Result holds the redacted text and a count of each PII type found.
type Result struct {
	Text  string
	Types map[string]int // e.g. {"KR_RRN": 1, "EMAIL": 2}
}

// Found returns true when at least one PII instance was detected.
func (r Result) Found() bool {
	for _, v := range r.Types {
		if v > 0 {
			return true
		}
	}
	return false
}

// New returns a Scanner initialised with built-in patterns for:
//   - Korean Resident Registration Numbers (KR_RRN) – checksum validated
//   - Email addresses (EMAIL)
//   - Korean mobile phone numbers (KR_PHONE)
func New() *Scanner {
	return &Scanner{
		patterns: []pattern{
			{
				typ: "KR_RRN",
				re:  regexp.MustCompile(`\b\d{6}-?[1-8]\d{6}\b`),
			},
			{
				typ: "EMAIL",
				re:  regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
			},
			{
				typ: "KR_PHONE",
				re:  regexp.MustCompile(`\b01[016789]-?\d{3,4}-?\d{4}\b`),
			},
		},
	}
}

// Redact replaces detected PII in text with placeholder tokens and returns
// a Result containing the sanitised text and per-type counts.
func (s *Scanner) Redact(text string) Result {
	if text == "" {
		return Result{Text: "", Types: map[string]int{}}
	}

	types := map[string]int{}
	out := text

	for _, p := range s.patterns {
		typ := p.typ
		re := p.re
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			if typ == "KR_RRN" && !isValidKRRN(m) {
				return m
			}
			types[typ]++
			return "[REDACTED:" + typ + "]"
		})
	}

	return Result{Text: out, Types: types}
}

// ---------- KR_RRN checksum ----------

func isValidKRRN(candidate string) bool {
	d := onlyDigits(candidate)
	if len(d) != 13 {
		return false
	}
	mm, _ := strconv.Atoi(d[2:4])
	dd, _ := strconv.Atoi(d[4:6])
	if mm < 1 || mm > 12 || dd < 1 || dd > 31 {
		return false
	}
	weights := []int{2, 3, 4, 5, 6, 7, 8, 9, 2, 3, 4, 5}
	sum := 0
	for i := 0; i < 12; i++ {
		sum += int(d[i]-'0') * weights[i]
	}
	check := (11 - (sum % 11)) % 10
	return check == int(d[12]-'0')
}

func onlyDigits(in string) string {
	var b strings.Builder
	for _, r := range in {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
