package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Event is one line of a journey's append-only log. Types starting "in." are
// things the user did; types starting "d." are what the engine derived from
// them. Each event carries the hash of the one before it.
type Event struct {
	Seq    int             `json:"seq"`
	TS     string          `json:"ts"`
	Type   string          `json:"type"`
	Target string          `json:"target,omitempty"`
	Data   json.RawMessage `json:"data"`
	Prev   string          `json:"prev"`
	Hash   string          `json:"hash"`
}

func (e Event) IsInput() bool { return strings.HasPrefix(e.Type, "in.") }

func (e Event) Time() time.Time {
	t, _ := time.Parse(time.RFC3339, e.TS)
	return t
}

func eventHash(e Event) string {
	e.Hash = ""
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func newEvent(prev []Event, now time.Time, typ, target string, data any) Event {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte(`{}`)
	}
	e := Event{Seq: len(prev) + 1, TS: stamp(now), Type: typ, Target: target, Data: raw}
	if len(prev) > 0 {
		e.Prev = prev[len(prev)-1].Hash
	}
	e.Hash = eventHash(e)
	return e
}

// VerifyChain reports the first event whose hash or link is wrong, or 0.
func VerifyChain(events []Event) (badSeq int, reason string) {
	prev := ""
	for i, e := range events {
		if e.Seq != i+1 {
			return i + 1, fmt.Sprintf("event %d is missing or out of order", i+1)
		}
		if e.Prev != prev {
			return e.Seq, fmt.Sprintf("event %d does not follow the event before it", e.Seq)
		}
		if eventHash(e) != e.Hash {
			return e.Seq, fmt.Sprintf("event %d was changed after it was recorded", e.Seq)
		}
		prev = e.Hash
	}
	return 0, ""
}

// SessionCode is a short form of the newest event's hash. If the code the
// user reads out matches the bundle, the bundle is complete.
func SessionCode(events []Event) string {
	if len(events) == 0 {
		return "0000-0000"
	}
	raw, _ := hex.DecodeString(events[len(events)-1].Hash)
	c := shortCode(raw, 8)
	return c[:4] + "-" + c[4:]
}

var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [REDACTED]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`), "[REDACTED]"},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16})\b`), "[REDACTED]"},
	{regexp.MustCompile(`(://[^/\s:@]+):[^/\s@]+@`), "$1:[REDACTED]@"},
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key)[A-Za-z0-9_.-]*)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`), "$1$2[REDACTED]"},
}

// Redact removes secret-looking values before anything is stored. The scripts
// otelgenie generates never print secrets; this protects against a user pasting
// more than the script's output.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			// The env file's placeholder is not a secret and must stay readable.
			if strings.HasSuffix(m, "CHANGE_ME") {
				return m
			}
			return r.re.ReplaceAllString(m, r.repl)
		})
	}
	return s
}
