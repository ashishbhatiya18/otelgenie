// Package engine is the whole of otelgenie's logic. It is pure: no storage, no
// clock, and no network except FetchSnapshot. The browser shell supplies time
// and persistence, which is what makes a recorded journey replayable.
package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Probe is one "#! discover" line: something to look for on the target and,
// optionally, the collector env variable the result fills.
type Probe struct {
	Kind string `json:"kind"` // port | unit | glob | input | ask
	Arg  string `json:"arg"`
	Var  string `json:"var,omitempty"`
	As   string `json:"as,omitempty"`
	Help string `json:"help,omitempty"`
}

// Need is one "#! needs" line: something outside the collector config that
// must exist (a credential, an exporter, a hand-out to an application owner).
type Need struct {
	Kind  string            `json:"kind"`
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

type VarRef struct {
	Name       string `json:"name"`
	Default    string `json:"default,omitempty"`
	HasDefault bool   `json:"has_default"`
}

// Entry is one catalog file: native collector config plus its "#!" header.
type Entry struct {
	Path      string   `json:"path"`
	Component string   `json:"component"`
	Signal    string   `json:"signal"`
	Title     string   `json:"title"`
	Targets   []string `json:"targets"`
	Always    bool     `json:"always"`
	Check     bool     `json:"check,omitempty"`
	Category  string   `json:"category,omitempty"`
	// When limits a file to projects of a certain shape. "servers" is a
	// cluster with things beside it that run on servers.
	When string `json:"when,omitempty"`
	// Role and Collector only matter in a cluster. Role is empty for
	// collector configuration, "collector" for the OpenTelemetryCollector
	// resource that configuration is merged into, and "manifest" for any
	// other resource applied as written. Collector says which of the two
	// collectors, "node" or "cluster", a file belongs to.
	Role      string            `json:"role,omitempty"`
	Collector string            `json:"collector,omitempty"`
	Probes    []Probe           `json:"probes,omitempty"`
	Needs     []Need            `json:"needs,omitempty"`
	Docs      map[string]string `json:"docs,omitempty"`
	Vars      []VarRef          `json:"vars,omitempty"`
	Receivers []string          `json:"receivers,omitempty"`
	Pipelines []string          `json:"pipelines,omitempty"`
	Body      string            `json:"-"`

	// receiverDefs is each receiver's configuration in a canonical form, to
	// tell a receiver shared by a component's files from a name collision.
	receiverDefs map[string]string
}

// pushed reports whether the data is sent by an application rather than
// collected by the collector, so nothing arrives until its owner acts.
func (e *Entry) pushed() bool {
	for _, n := range e.Needs {
		if n.Kind == "handout" {
			return true
		}
	}
	// OTLP is only ever sent to; nothing is fetched through it.
	for _, r := range e.Receivers {
		if !strings.HasPrefix(r, "otlp/") {
			return false
		}
	}
	return len(e.Receivers) > 0
}

type Catalog struct {
	Entries []*Entry
}

var (
	envRef    = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)
	nameOK    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	signalSet = map[string]bool{"base": true, "metrics": true, "logs": true, "events": true, "traces": true}
	inputSet  = map[string]bool{"endpoint": true, "project": true, "env": true, "memory_limit_mib": true, "ca_file": true, "target": true,
		"header_dimensions": true, "request_headers": true, "response_headers": true}
)

// splitFields splits on spaces, keeping double-quoted runs together.
func splitFields(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			has = true
		case (r == ' ' || r == '\t') && !inQuote:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

func keyVals(fields []string) (map[string]string, []string) {
	kv := map[string]string{}
	var order []string
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		kv[k] = v
		order = append(order, k)
	}
	return kv, order
}

// ParseEntry reads one catalog file. Problems are lint findings; an entry with
// problems is still returned so every finding can be reported at once.
func ParseEntry(path, text string) (*Entry, []string) {
	e := &Entry{Path: path, Body: text, Docs: map[string]string{}}
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, path+": "+fmt.Sprintf(format, a...)) }

	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "#! ") {
			continue
		}
		fields := splitFields(strings.TrimPrefix(line, "#! "))
		if len(fields) == 0 {
			continue
		}
		rest := fields[1:]
		switch fields[0] {
		case "otelgenie":
			kv, _ := keyVals(rest)
			e.Component, e.Signal, e.Title = kv["component"], kv["signal"], kv["title"]
			if kv["targets"] != "" {
				e.Targets = strings.Split(kv["targets"], ",")
			}
			e.Always = kv["always"] == "true"
			e.Check = kv["check"] == "true"
			e.Role, e.Collector = kv["role"], kv["collector"]
			e.Category = kv["category"]
			if e.When = kv["when"]; e.When != "" && e.When != whenServers {
				bad("when %q must be %s", e.When, whenServers)
			}
		case "discover":
			p := Probe{}
			arrow := -1
			for i, f := range rest {
				if f == "->" {
					arrow = i
				}
			}
			head := rest
			if arrow >= 0 {
				head = rest[:arrow]
				if arrow+1 < len(rest) {
					p.Var = rest[arrow+1]
					attrs, _ := keyVals(rest[arrow+2:])
					p.As, p.Help = attrs["as"], attrs["help"]
				}
			}
			kv, order := keyVals(head)
			if len(order) != 1 {
				bad("discover needs exactly one probe, got %q", line)
				continue
			}
			p.Kind, p.Arg = order[0], kv[order[0]]
			switch p.Kind {
			case "port", "unit", "glob", "distro", "svc":
			case "pod":
				if p.Var == "" {
					bad("discover pod must fill a variable")
				}
			case "input":
				if !inputSet[p.Arg] {
					bad("unknown input %q", p.Arg)
				}
			case "ask":
				p.Help = p.Arg
				if p.Var == "" {
					bad("discover ask must fill a variable")
				}
			default:
				bad("unknown discover kind %q", p.Kind)
			}
			e.Probes = append(e.Probes, p)
		case "needs":
			kv, order := keyVals(rest)
			if len(order) == 0 {
				bad("empty needs line")
				continue
			}
			n := Need{Kind: order[0], Name: kv[order[0]], Attrs: map[string]string{}}
			for _, k := range order[1:] {
				n.Attrs[k] = kv[k]
			}
			e.Needs = append(e.Needs, n)
		case "docs":
			kv, _ := keyVals(rest)
			for k, v := range kv {
				e.Docs[k] = v
			}
		default:
			bad("unknown directive %q", fields[0])
		}
	}

	if !nameOK.MatchString(e.Component) {
		bad("component name %q must be lowercase letters, digits and dashes", e.Component)
	}
	if !signalSet[e.Signal] && !(e.Signal == "any" && e.Role != "") {
		bad("signal %q must be one of base, metrics, logs, events, traces", e.Signal)
	}
	if e.Role != "" && e.Role != roleCollector && e.Role != roleManifest && e.Role != roleGuide {
		bad("role %q must be collector, manifest or guide", e.Role)
	}
	if e.Role == roleGuide {
		// Instructions for people, not something installed on a target.
		if len(e.Targets) > 0 || e.Collector != "" {
			bad("a guide belongs to no target and no collector")
		}
	} else if contains(e.Targets, kindK8s) {
		switch {
		case len(e.Targets) > 1:
			bad("a file for a cluster cannot also be for another target")
		case e.Role == roleManifest || e.Signal == "base":
			if e.Collector != "" && e.Role == roleManifest {
				bad("a manifest belongs to no collector")
			}
		case e.Collector != "node" && e.Collector != "cluster":
			bad("collector must be node or cluster")
		}
	} else if e.Role != "" || e.Collector != "" {
		bad("role and collector only apply to targets=k8s")
	}
	if len(e.Targets) == 0 && e.Role != roleGuide {
		bad("no targets declared")
	}
	if e.Title == "" {
		bad("no title")
	}

	seen := map[string]bool{}
	for _, m := range envRef.FindAllStringSubmatch(text, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		e.Vars = append(e.Vars, VarRef{Name: m[1], Default: m[2], HasDefault: strings.Contains(m[0], ":-")})
	}
	declared := map[string]bool{}
	for _, p := range e.Probes {
		if p.Var != "" {
			declared[p.Var] = true
		}
		if p.Kind == "pod" {
			// Set on the pod for components that read the environment
			// themselves, so it need not appear as ${env:...}.
			seen[p.Var] = true
		}
	}
	for _, n := range e.Needs {
		if n.Kind == "credential" {
			declared[n.Name] = true
		}
	}
	for _, v := range e.Vars {
		if !declared[v.Name] {
			bad("${env:%s} is used but no discover or needs line declares it", v.Name)
		}
	}
	for name := range declared {
		if !seen[name] {
			bad("%s is declared in the header but never used", name)
		}
	}

	if e.Role == roleGuide {
		return e, problems
	}
	if e.Role != "" {
		// Kubernetes resources, possibly several in one file.
		docs, err := yamlDocs(text)
		if err != nil {
			bad("not valid YAML: %v", err)
			return e, problems
		}
		if e.Role == roleCollector {
			found := 0
			for _, d := range docs {
				if d["kind"] == "OpenTelemetryCollector" {
					found++
				}
			}
			if found != 1 {
				bad("a role=collector file must hold exactly one OpenTelemetryCollector")
			}
		}
		return e, problems
	}

	var doc struct {
		Connectors map[string]any `yaml:"connectors"`
		Receivers  map[string]any `yaml:"receivers"`
		Service    struct {
			Pipelines map[string]any `yaml:"pipelines"`
		} `yaml:"service"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		bad("not valid YAML: %v", err)
		return e, problems
	}
	e.receiverDefs = map[string]string{}
	for name, def := range doc.Receivers {
		e.Receivers = append(e.Receivers, name)
		raw, _ := yaml.Marshal(def)
		e.receiverDefs[name] = string(raw)
	}
	for name := range doc.Service.Pipelines {
		e.Pipelines = append(e.Pipelines, name)
	}
	sort.Strings(e.Receivers)
	sort.Strings(e.Pipelines)
	if e.Signal != "base" {
		// Separate files are merged by the collector, which replaces lists.
		// Suffixing every name with the component keeps files from colliding.
		for _, r := range e.Receivers {
			if _, suffix, ok := strings.Cut(r, "/"); !ok || !strings.HasPrefix(suffix, e.Component) {
				bad("receiver %q must be named <type>/%s", r, e.Component)
			}
		}
		if len(e.Pipelines) == 0 {
			bad("no pipeline defined")
		}
		wantType := e.Signal
		if wantType == "events" {
			wantType = "logs"
		}
		for _, p := range e.Pipelines {
			typ, suffix, ok := strings.Cut(p, "/")
			// A connector turns one signal into another, so a file with one
			// needs a pipeline on each side of it.
			bridged := len(doc.Connectors) > 0 && (typ == "traces" || typ == "metrics" || typ == "logs")
			if !ok || (typ != wantType && !bridged) || !strings.HasPrefix(suffix, e.Component) {
				bad("pipeline %q must be named %s/%s", p, wantType, e.Component)
			}
		}
		for c := range doc.Connectors {
			if _, suffix, ok := strings.Cut(c, "/"); !ok || !strings.HasPrefix(suffix, e.Component) {
				bad("connector %q must be named <type>/%s", c, e.Component)
			}
		}
	}
	return e, problems
}

// LoadCatalog parses every file under catalog/ in the snapshot.
func LoadCatalog(files map[string]string) (*Catalog, []string) {
	var paths []string
	for p := range files {
		if strings.HasPrefix(p, "catalog/") && (strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".md")) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	c := &Catalog{}
	var problems []string
	names := map[string]*Entry{}
	for _, p := range paths {
		e, probs := ParseEntry(p, files[p])
		problems = append(problems, probs...)
		// A name only collides with files that can land on the same target.
		scope := strings.Join(e.Targets, ",") + " "
		for _, n := range e.Receivers {
			n = scope + n
			// One component may take several signals through a single receiver,
			// as OTLP does. Each of its files then repeats the receiver word
			// for word, which the collector's merge leaves as one receiver.
			other, dup := names[n]
			bare := strings.TrimPrefix(n, scope)
			if dup && (other.Component != e.Component || other.Collector != e.Collector || other.receiverDefs[bare] != e.receiverDefs[bare]) {
				problems = append(problems, fmt.Sprintf("%s: %q is also defined in %s", p, bare, other.Path))
			}
			names[n] = e
		}
		for _, n := range e.Pipelines {
			if other, dup := names[scope+n]; dup {
				problems = append(problems, fmt.Sprintf("%s: %q is also defined in %s", p, n, other.Path))
			}
			names[scope+n] = e
		}
		c.Entries = append(c.Entries, e)
	}
	if len(c.Select("vm", nil, nil)) == 0 {
		problems = append(problems, "catalog has no base entry for target vm")
	}
	return c, problems
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Select returns the entries to deploy: base, then everything matching the
// chosen components and signals. Always-on components need no selection.
func (c *Catalog) Select(kind string, components, signals []string) []*Entry {
	var out []*Entry
	for _, e := range c.Entries {
		if !contains(e.Targets, kind) {
			continue
		}
		if e.Signal == "base" {
			out = append(out, e)
			continue
		}
		if e.Signal != "any" && !contains(signals, e.Signal) {
			continue
		}
		if e.Always || contains(components, e.Component) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		bi, bj := out[i].Signal == "base", out[j].Signal == "base"
		if bi != bj {
			return bi
		}
		return out[i].Path < out[j].Path
	})
	return out
}

type ComponentInfo struct {
	Name   string `json:"name"`
	Always bool   `json:"always"`
	// Check marks a general-purpose check on the server rather than a piece
	// of software the project runs, so the page can list the two apart.
	Check bool `json:"check"`
	// Platform marks a kind of cluster: the component that owns the
	// collectors everything else in that cluster is merged into.
	Platform bool     `json:"platform"`
	Targets  []string `json:"targets"`
	// Category groups components where the user picks them, for example
	// "Databases". It is whatever the component's files say.
	Category string   `json:"category"`
	Signals  []string `json:"signals"`
	Titles   []string `json:"titles"`
}

// Components lists what the user can pick for a target kind, or for any
// kind when it is empty.
func (c *Catalog) Components(kind string) []ComponentInfo {
	by := map[string]*ComponentInfo{}
	var order []string
	for _, e := range c.Entries {
		if e.Signal == "base" || (kind != "" && !contains(e.Targets, kind)) {
			continue
		}
		ci, ok := by[e.Component]
		if !ok {
			ci = &ComponentInfo{Name: e.Component, Signals: []string{}, Targets: []string{}}
			by[e.Component] = ci
			order = append(order, e.Component)
		}
		ci.Always = ci.Always || e.Always
		ci.Check = ci.Check || e.Check
		ci.Platform = ci.Platform || e.Role == roleCollector
		if ci.Category == "" {
			ci.Category = e.Category
		}
		for _, t := range e.Targets {
			if !contains(ci.Targets, t) {
				ci.Targets = append(ci.Targets, t)
			}
		}
		if e.Signal != "any" && !contains(ci.Signals, e.Signal) {
			ci.Signals = append(ci.Signals, e.Signal)
		}
		ci.Titles = append(ci.Titles, e.Title)
	}
	sort.Strings(order)
	out := make([]ComponentInfo, 0, len(order))
	for _, n := range order {
		out = append(out, *by[n])
	}
	return out
}
