package governance

import (
	"regexp"
	"strings"
)

// ── Redactor ──────────────────────────────────────────────────
//
// Scans text for PII and secret patterns and replaces them with
// labelled placeholders before content is written to memory or logs.
//
// Patterns covered:
//   PII    — email addresses, US phone numbers, US SSNs, credit cards
//   Secrets — generic API keys, AWS access/secret keys, JWT tokens,
//             private keys (PEM), GitHub tokens, Anthropic API keys

type redactRule struct {
	label   string
	pattern *regexp.Regexp
}

// placeholder format: [REDACTED:<LABEL>]
func placeholder(label string) string { return "[REDACTED:" + label + "]" }

var rules = []redactRule{
	// ── PII ────────────────────────────────────────────────────
	{
		label: "EMAIL",
		// RFC 5321-ish; captures the common case
		pattern: regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
	},
	{
		label: "PHONE_US",
		// Matches: (555) 867-5309, 555-867-5309, +1 555 867 5309, etc.
		pattern: regexp.MustCompile(`(?:\+1[\s\-.]?)?\(?\d{3}\)?[\s\-.]?\d{3}[\s\-.]?\d{4}`),
	},
	{
		label: "SSN",
		// US Social Security Number: 123-45-6789 or 123456789
		pattern: regexp.MustCompile(`\b\d{3}[-\s]?\d{2}[-\s]?\d{4}\b`),
	},
	{
		label: "CREDIT_CARD",
		// Visa/MC/Amex/Discover (with or without dashes/spaces, Luhn not validated)
		pattern: regexp.MustCompile(`\b(?:4\d{3}|5[1-5]\d{2}|3[47]\d{2}|6(?:011|5\d{2}))[\s\-]?\d{4}[\s\-]?\d{4}[\s\-]?\d{4,7}\b`),
	},

	// ── Secrets ────────────────────────────────────────────────
	{
		label: "ANTHROPIC_KEY",
		// sk-ant-... prefixed keys
		pattern: regexp.MustCompile(`sk-ant-[A-Za-z0-9\-_]{20,}`),
	},
	{
		label: "OPENAI_KEY",
		pattern: regexp.MustCompile(`sk-[A-Za-z0-9]{32,}`),
	},
	{
		label: "AWS_ACCESS_KEY",
		// AWS access key IDs start with AKIA or ASIA
		pattern: regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`),
	},
	{
		label: "AWS_SECRET_KEY",
		// 40-char base64 string following common label patterns
		pattern: regexp.MustCompile(`(?i)(?:aws.?secret.?(?:access.?)?key|aws_secret)[^\n=]*=\s*([A-Za-z0-9/+=]{40})`),
	},
	{
		label: "GITHUB_TOKEN",
		pattern: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`),
	},
	{
		label: "JWT",
		// Standard JWT: three base64url sections separated by dots
		pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
	},
	{
		label: "PEM_PRIVATE_KEY",
		// PEM block start
		pattern: regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	},
	{
		label: "GENERIC_SECRET",
		// Catch-all: variable assignment patterns with long random-ish values
		pattern: regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key|auth[_-]?key)\s*[=:]\s*['"]?([A-Za-z0-9!@#$%^&*()\-_=+\[\]{}|;:,.<>?/]{16,})['"]?`),
	},
}

// Redactor scans and rewrites sensitive content.
type Redactor struct {
	rules []redactRule
}

// NewRedactor constructs a Redactor with the default rule set.
func NewRedactor() *Redactor {
	return &Redactor{rules: rules}
}

// Redact replaces all matched patterns in text with labelled placeholders.
// It applies rules in order; a single position can only be replaced once
// (the first matching rule wins for overlapping matches).
func (r *Redactor) Redact(text string) string {
	for _, rule := range r.rules {
		text = rule.pattern.ReplaceAllStringFunc(text, func(match string) string {
			return placeholder(rule.label)
		})
	}
	return text
}

// RedactMap applies Redact to all string values in a map (one level deep).
func (r *Redactor) RedactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case string:
			out[k] = r.Redact(val)
		default:
			out[k] = v
		}
	}
	return out
}

// ContainsSensitive returns true if the text matches any redaction rule.
// Useful as a quick pre-check before deciding whether to quarantine a
// memory record.
func (r *Redactor) ContainsSensitive(text string) bool {
	for _, rule := range r.rules {
		if rule.pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// MatchedLabels returns the set of rule labels that fire on the given text.
func (r *Redactor) MatchedLabels(text string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, rule := range r.rules {
		if rule.pattern.MatchString(text) && !seen[rule.label] {
			seen[rule.label] = true
			out = append(out, rule.label)
		}
	}
	return out
}

// RedactJSON is a convenience function that redacts a JSON string by applying
// the pattern replacements directly in the raw text. This avoids a
// marshal/unmarshal round-trip for logging pipelines.
func (r *Redactor) RedactJSON(jsonStr string) string {
	return r.Redact(jsonStr)
}

// ── Prompt-injection heuristics ───────────────────────────────
//
// These are lightweight checks layered on top of the anomaly detector.
// They do not guarantee detection of all injection attempts.

var injectionPatterns = []*regexp.Regexp{
	// Classic "ignore previous instructions" variants
	regexp.MustCompile(`(?i)ignore\s+(?:all\s+)?(?:previous|prior|above)\s+instructions?`),
	// "You are now..."
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(?:a|an|the)\s+\w+`),
	// Jailbreak framing
	regexp.MustCompile(`(?i)(?:act\s+as|pretend\s+(?:you\s+are|to\s+be)|roleplay\s+as)\s+(?:a|an)?\s*(?:jailbreak|dan|evil|unfiltered|unrestricted|hacker|malicious)`),
	// System prompt overrides
	regexp.MustCompile(`(?i)<\s*(?:system|sys)\s*>`),
	regexp.MustCompile(`(?i)\[INST\]|\[\/?SYS\]|<<SYS>>`),
	// Base64 decode calls (common exfiltration pattern)
	regexp.MustCompile(`(?i)base64\.decode|atob\s*\(`),
}

// LooksLikeInjection returns true when text contains common prompt-injection
// markers. False positives are possible; use as one signal among several.
func LooksLikeInjection(text string) bool {
	lower := strings.ToLower(text)
	for _, p := range injectionPatterns {
		if p.MatchString(lower) {
			return true
		}
	}
	return false
}
