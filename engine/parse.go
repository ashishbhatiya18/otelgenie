package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Block is the output of one of the generated scripts, pasted back by the
// user: KEY=VALUE lines between a start marker and an end marker.
type Block struct {
	Kind   string
	Values map[string]string
	Keys   []string
}

func (b *Block) Get(key string) string { return b.Values[key] }

// WithPrefix returns the keys starting with prefix, in the order printed.
func (b *Block) WithPrefix(prefix string) []string {
	var out []string
	for _, k := range b.Keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// ParseBlock finds the block of the given kind in pasted text. The messages
// are written for the user, since a bad paste is the most common stumble.
func ParseBlock(text, kind string) (*Block, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("nothing was pasted")
	}
	start := "OTELGENIE-" + kind + " v1"
	end := "END OTELGENIE-" + kind + " lines="
	lines := strings.Split(text, "\n")
	from := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == start {
			from = i
		}
	}
	if from < 0 {
		for _, other := range []string{"DISCOVER", "APPLY", "VERIFY", "ROLLBACK"} {
			if other != kind && strings.Contains(text, "OTELGENIE-"+other+" v1") {
				return nil, fmt.Errorf("this is the output of the %s script, but this step needs the output of the %s script", strings.ToLower(other), strings.ToLower(kind))
			}
		}
		return nil, fmt.Errorf("could not find the line %q. Paste everything the script printed, starting from that line", start)
	}
	b := &Block{Kind: kind, Values: map[string]string{}}
	for _, l := range lines[from+1:] {
		l = strings.TrimRight(l, " \t")
		if strings.HasPrefix(strings.TrimSpace(l), end) {
			want, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(l), end))
			if err != nil || want != len(b.Keys) {
				return nil, fmt.Errorf("the output looks incomplete: the script printed %s lines but %d arrived. Copy it again", strings.TrimPrefix(strings.TrimSpace(l), end), len(b.Keys))
			}
			return b, nil
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		if _, dup := b.Values[k]; !dup {
			b.Keys = append(b.Keys, k)
		}
		b.Values[k] = v
	}
	return nil, fmt.Errorf("the output is cut off: the last line should start with %q. Copy it again, down to the last line", strings.TrimSpace(end))
}

// Diagnosis turns a raw error line into what is wrong and what to do.
type Diagnosis struct {
	Cause string `json:"cause"`
	Fix   string `json:"fix"`
}

var doctorRules = []struct {
	re *regexp.Regexp
	d  Diagnosis
}{
	{regexp.MustCompile(`(?i)x509|certificate signed by unknown authority|certificate is not trusted`),
		Diagnosis{"The server does not trust the certificate of the collector endpoint.", "The CA bundle is missing or is the wrong one. Check the CA file chosen during setup, generate the files again and re-apply."}},
	{regexp.MustCompile(`(?i)\b401\b|unauthorized|unauthenticated|invalid token`),
		Diagnosis{"The collector endpoint rejected the access token.", "Re-run apply.sh after removing the OTEL_ACCESS_TOKEN line from /etc/otelcol-otelgenie/otelcol.env, and enter the token again. If it still fails, ask whoever issued the token to confirm it is valid for this project and environment."}},
	{regexp.MustCompile(`(?i)\b403\b|permission denied.*export|forbidden`),
		Diagnosis{"The collector endpoint accepted the token but refused the data.", "The token is not allowed to write for this project and environment. Ask whoever issued it."}},
	{regexp.MustCompile(`(?i)no such host|server misbehaving|name resolution`),
		Diagnosis{"A host name could not be resolved from this server.", "Check the spelling of the address. If it is right, the server's DNS does not know this name; ask the network or platform team."}},
	{regexp.MustCompile(`(?i)password authentication failed|authentication failed for user`),
		Diagnosis{"The monitoring user's password was rejected.", "Ask the database owner to confirm the monitoring user and password, then enter it again on the server."}},
	{regexp.MustCompile(`(?i)connection refused`),
		Diagnosis{"Nothing is listening at the address the collector tried to reach.", "If this is a database exporter, it is not running or is on a different port; ask its owner. If this is the collector endpoint, check the address and port."}},
	{regexp.MustCompile(`(?i)i/o timeout|deadline exceeded|context deadline|timed out`),
		Diagnosis{"The connection timed out, which usually means a firewall is dropping it.", "Ask the network team to allow this server to reach that address and port."}},
	{regexp.MustCompile(`(?i)permission denied|operation not permitted`),
		Diagnosis{"The collector is not allowed to read a file or directory.", "Check which user the service runs as and that it can read the path named in the message."}},
	{regexp.MustCompile(`(?i)address already in use`),
		Diagnosis{"A port the collector needs is already taken on this server.", "Another collector or program is using it. Stop the other one, or ask for the port in the template to be changed."}},
	{regexp.MustCompile(`(?i)memory usage|data refused due to high memory|oom|out of memory`),
		Diagnosis{"The collector hit its memory limit.", "It is dropping data to stay within the limit. Ask for a higher memory limit in the template, or monitor fewer components on this server."}},
	{regexp.MustCompile(`(?i)unknown type|invalid keys|has invalid keys|unknown receivers|unknown exporters|unknown processors|cannot unmarshal`),
		Diagnosis{"The installed collector does not understand part of the configuration.", "The collector version on the server does not match the template. Install the version listed in the artifact list."}},
	{regexp.MustCompile(`(?i)journalctl.*not found|journald is only supported`),
		Diagnosis{"This server has no systemd journal to read.", "Deselect logs and events for the server itself, or use a server that runs systemd."}},
	{regexp.MustCompile(`(?i)must not be empty|missing required|environment variable .* (is unset|not set)|CHANGE_ME`),
		Diagnosis{"A value the configuration needs was left empty.", "Open /etc/otelcol-otelgenie/otelcol.env on the server, fill in the value named in the message, then run apply.sh again."}},
	{regexp.MustCompile(`(?i)no files match|no such file or directory`),
		Diagnosis{"A file or directory in the configuration does not exist on this server.", "Run discovery again so the paths are picked up from this server, or correct the path when confirming components."}},
}

// Diagnose returns nil when no rule recognises the line.
func Diagnose(line string) *Diagnosis {
	for _, r := range doctorRules {
		if r.re.MatchString(line) {
			d := r.d
			return &d
		}
	}
	return nil
}

var (
	componentID   = regexp.MustCompile(`"otelcol\.component\.id":\s*"([^"]+)"`)
	componentKind = regexp.MustCompile(`"otelcol\.component\.kind":\s*"([^"]+)"`)
)

// errorComponent extracts which receiver or exporter a collector log line is
// about, when it says.
func errorComponent(line string) (id, kind string) {
	if m := componentID.FindStringSubmatch(line); m != nil {
		id = m[1]
	}
	if m := componentKind.FindStringSubmatch(line); m != nil {
		kind = m[1]
	}
	return id, kind
}
