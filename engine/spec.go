package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// What a header found in an API description is good for.
const (
	HeaderBreakdown = "breakdown" // a small set of values: split request metrics by it
	HeaderValue     = "value"     // a number: recorded on each request, not turned into a metric
	HeaderUnique    = "unique"    // different on every request: no use as a metric
	HeaderSecret    = "secret"    // a credential: never captured
)

// SpecHeader is one header an API description declares, and what telemetry
// can be made from it.
type SpecHeader struct {
	Name       string   `json:"name"`
	In         string   `json:"in"` // request | response
	Type       string   `json:"type,omitempty"`
	Values     []string `json:"values,omitempty"`
	Operations []string `json:"operations"`
	Kind       string   `json:"kind"`
	Metric     string   `json:"metric"`
}

type SpecReport struct {
	Title      string       `json:"title"`
	Version    string       `json:"version"`
	Operations int          `json:"operations"`
	Headers    []SpecHeader `json:"headers"`
}

var (
	secretHeader  = regexp.MustCompile(`^(authorization|proxy-authorization|cookie|set-cookie|www-authenticate)$|token|secret|password|passwd|api-?key|signature|credential|session`)
	uniqueHeader  = regexp.MustCompile(`request-?id|correlation|trace-?id|span-?id|^traceparent$|^tracestate$|^x-b3-|idempotency|^etag$|^if-|^date$|timestamp|^last-modified$|^location$|^link$|^expires$|nonce|^x-amz-date$`)
	numericHeader = regexp.MustCompile(`ratelimit|rate-limit|^retry-after$|^content-length$|^age$|response-time|-count$|-total$|-size$|-remaining$|-limit$|duration|elapsed`)
	httpMethods   = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}
)

// classify decides what a header is good for from its name, its declared
// type and whether the description lists its possible values.
func classify(h *SpecHeader) {
	name := strings.ToLower(h.Name)
	switch {
	case secretHeader.MatchString(name):
		h.Kind, h.Metric = HeaderSecret, "Never captured: it carries a credential."
	case uniqueHeader.MatchString(name):
		h.Kind, h.Metric = HeaderUnique, "Not used: it is different on every request, so it cannot group anything. It stays visible on individual traces."
	case len(h.Values) == 0 && (h.Type == "integer" || h.Type == "number" || numericHeader.MatchString(name)):
		h.Kind, h.Metric = HeaderValue, "Recorded on every request as a number you can chart from traces. Not a metric of its own in this setup."
	default:
		h.Kind = HeaderBreakdown
		h.Metric = "Request count, error count and duration, split by " + h.Name + "."
		if len(h.Values) > 0 {
			h.Metric += " Values: " + strings.Join(h.Values, ", ") + "."
		} else {
			h.Metric += " Only worth it if it takes a small set of values."
		}
	}
}

// AnalyzeSpec reads a Swagger 2 or OpenAPI 3 description in JSON and lists
// every header it declares.
func AnalyzeSpec(text string) (*SpecReport, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("this is not valid JSON; export the API description as swagger.json or openapi.json")
	}
	if doc["swagger"] == nil && doc["openapi"] == nil {
		return nil, fmt.Errorf("this JSON is not a Swagger or OpenAPI description: it has no \"swagger\" or \"openapi\" field")
	}
	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		return nil, fmt.Errorf("the description lists no paths")
	}
	rep := &SpecReport{}
	if info, ok := doc["info"].(map[string]any); ok {
		rep.Title, _ = info["title"].(string)
		rep.Version, _ = info["version"].(string)
	}

	// resolve follows a "#/a/b/c" reference inside the same document.
	var resolve func(v any, depth int) map[string]any
	resolve = func(v any, depth int) map[string]any {
		m, _ := v.(map[string]any)
		ref, _ := m["$ref"].(string)
		if ref == "" || depth > 8 || !strings.HasPrefix(ref, "#/") {
			return m
		}
		var cur any = doc
		for _, part := range strings.Split(ref[2:], "/") {
			part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
			next, _ := cur.(map[string]any)
			cur = next[part]
		}
		return resolve(cur, depth+1)
	}

	found := map[string]*SpecHeader{}
	note := func(name, in, op string, def map[string]any) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := in + " " + strings.ToLower(name)
		h := found[key]
		if h == nil {
			h = &SpecHeader{Name: name, In: in}
			found[key] = h
		}
		if !contains(h.Operations, op) {
			h.Operations = append(h.Operations, op)
		}
		// OpenAPI 3 keeps the type under "schema"; Swagger 2 has it inline.
		schema := def
		if s := resolve(def["schema"], 0); s != nil {
			schema = s
		}
		if t, _ := schema["type"].(string); t != "" && h.Type == "" {
			h.Type = t
		}
		if enum, _ := schema["enum"].([]any); len(enum) > 0 && len(h.Values) == 0 {
			for _, v := range enum {
				h.Values = append(h.Values, fmt.Sprint(v))
			}
		}
	}
	params := func(list any, op string) {
		items, _ := list.([]any)
		for _, raw := range items {
			p := resolve(raw, 0)
			if in, _ := p["in"].(string); in == "header" {
				name, _ := p["name"].(string)
				note(name, "request", op, p)
			}
		}
	}

	for _, path := range sortedKeys(paths) {
		item := resolve(paths[path], 0)
		for _, method := range httpMethods {
			op := resolve(item[method], 0)
			if op == nil {
				continue
			}
			rep.Operations++
			label := strings.ToUpper(method) + " " + path
			params(item["parameters"], label)
			params(op["parameters"], label)
			responses, _ := op["responses"].(map[string]any)
			for _, code := range sortedKeys(responses) {
				headers, _ := resolve(responses[code], 0)["headers"].(map[string]any)
				for _, name := range sortedKeys(headers) {
					note(name, "response", label, resolve(headers[name], 0))
				}
			}
		}
	}

	// A key sent in a header to authenticate is a header too, and a secret.
	schemes, _ := doc["securityDefinitions"].(map[string]any)
	if comps, ok := doc["components"].(map[string]any); ok {
		if s, ok := comps["securitySchemes"].(map[string]any); ok {
			schemes = s
		}
	}
	for _, name := range sortedKeys(schemes) {
		s := resolve(schemes[name], 0)
		if in, _ := s["in"].(string); in == "header" {
			header, _ := s["name"].(string)
			note(header, "request", "every secured operation", s)
			if h := found["request "+strings.ToLower(strings.TrimSpace(header))]; h != nil {
				h.Kind, h.Metric = HeaderSecret, "Never captured: it carries a credential."
			}
		}
	}

	for _, key := range sortedKeys(found) {
		h := found[key]
		if h.Kind == "" {
			classify(h)
		}
		rep.Headers = append(rep.Headers, *h)
	}
	order := map[string]int{HeaderBreakdown: 0, HeaderValue: 1, HeaderUnique: 2, HeaderSecret: 3}
	sort.SliceStable(rep.Headers, func(a, b int) bool { return order[rep.Headers[a].Kind] < order[rep.Headers[b].Kind] })
	return rep, nil
}
