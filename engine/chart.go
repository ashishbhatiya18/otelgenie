package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A microservice in a cluster is instrumented by an annotation on its pod
// template. Most services are installed from a Helm chart, so the annotation
// has to go in through the chart: either a value the chart already passes to
// the pod template, or an edit to the template itself. Reading the chart
// tells which.

// ChartWorkload is one template in a chart that runs pods.
type ChartWorkload struct {
	Kind string `json:"kind"`
	File string `json:"file"`
	// Key is the value the template fills the pods' annotations from, such
	// as podAnnotations. Empty when the template takes none.
	Key string `json:"key,omitempty"`
}

type ChartReport struct {
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Workloads []ChartWorkload `json:"workloads"`
	// Injected is set when the chart already carries an inject annotation.
	Injected bool `json:"injected"`
}

const (
	chartFileLimit  = 1 << 20
	chartTotalLimit = 8 << 20
	injectPrefix    = "instrumentation.opentelemetry.io/inject-"
)

var (
	chartText      = regexp.MustCompile(`\.(ya?ml|tpl|txt)$`)
	workloadKind   = regexp.MustCompile(`(?m)^kind:\s*(Deployment|StatefulSet|DaemonSet|Rollout|CronJob)\s*$`)
	podTemplate    = regexp.MustCompile(`(?m)^\s+template:\s*$`)
	annotationsKey = regexp.MustCompile(`\.Values\.([A-Za-z0-9_.]*[Aa]nnotations)\b`)
)

// UnpackChart reads the text files out of a packaged chart (helm package).
func UnpackChart(raw []byte) (map[string]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("this is not a packaged chart (.tgz)")
	}
	files, total := map[string]string{}, 0
	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the packaged chart could not be read")
		}
		if h.Typeflag != tar.TypeReg || !chartText.MatchString(h.Name) || h.Size > chartFileLimit {
			continue
		}
		if total += int(h.Size); total > chartTotalLimit {
			return nil, fmt.Errorf("the chart is too large to read")
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("the packaged chart could not be read")
		}
		files[h.Name] = string(body)
	}
	return files, nil
}

// AnalyzeChart finds the workloads in a chart and how their pods can be
// given an annotation. Files are keyed by their path inside the chart or
// inside the folder that holds it.
func AnalyzeChart(files map[string]string) (*ChartReport, error) {
	root, found := "", false
	for name := range files {
		name = path.Clean(strings.ReplaceAll(name, `\`, "/"))
		if path.Base(name) != "Chart.yaml" {
			continue
		}
		dir := strings.TrimSuffix(name, "Chart.yaml")
		// The chart itself, not a chart packed inside it.
		if !found || len(dir) < len(root) {
			root, found = dir, true
		}
	}
	if !found {
		return nil, fmt.Errorf("this is not a Helm chart: there is no Chart.yaml in it")
	}
	rep := &ChartReport{Workloads: []ChartWorkload{}}
	var names []string
	for name, body := range files {
		clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
		if !strings.HasPrefix(clean, root) {
			continue
		}
		rel := strings.TrimPrefix(clean, root)
		switch {
		case rel == "Chart.yaml":
			var meta struct{ Name, Version string }
			if yaml.Unmarshal([]byte(body), &meta) != nil || meta.Name == "" {
				return nil, fmt.Errorf("the chart's Chart.yaml could not be read")
			}
			rep.Name, rep.Version = meta.Name, meta.Version
		case rel == "values.yaml":
			rep.Injected = rep.Injected || strings.Contains(body, injectPrefix)
		case strings.HasPrefix(rel, "templates/"):
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		rep.Injected = rep.Injected || strings.Contains(body, injectPrefix)
		m := workloadKind.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		w := ChartWorkload{Kind: m[1], File: strings.TrimPrefix(path.Clean(strings.ReplaceAll(name, `\`, "/")), root)}
		// Annotations above the pod template belong to the workload, not its pods.
		if at := podTemplate.FindStringIndex(body); at != nil {
			if k := annotationsKey.FindStringSubmatch(body[at[1]:]); k != nil {
				w.Key = k[1]
			}
		}
		rep.Workloads = append(rep.Workloads, w)
	}
	return rep, nil
}

// ServiceChart is what a project keeps of a microservice's chart.
type ServiceChart struct {
	Name      string          `json:"name"`
	Workloads []ChartWorkload `json:"workloads,omitempty"`
}

var languages = map[string]string{"java": "Java", "python": "Python", "go": "Go"}

// goTargetAnnotation names the binary the Go agent attaches to; only the
// service's owner knows it.
const (
	goTargetAnnotation  = "instrumentation.opentelemetry.io/otel-go-auto-target-exe"
	goTargetPlaceholder = "/PATH/TO/THE/BINARY"
)

// language is the agent a service gets; Java unless it says otherwise.
func (s Service) language() string {
	if _, ok := languages[s.Language]; ok {
		return s.Language
	}
	return "java"
}

func (s Service) injectAnnotation() string { return injectPrefix + s.language() }

// annotations are everything the pod template needs, in the order to write them.
func (s Service) annotations() [][2]string {
	out := [][2]string{{s.injectAnnotation(), k8sNamespace + "/otelgenie"}}
	if s.language() == "go" {
		out = append(out, [2]string{goTargetAnnotation, goTargetPlaceholder})
	}
	return out
}

// chartKeys are the values that reach the pods' annotations in the service's chart.
func (s Service) chartKeys() []string {
	var keys []string
	if s.Chart != nil {
		for _, w := range s.Chart.Workloads {
			if w.Key != "" && !contains(keys, w.Key) {
				keys = append(keys, w.Key)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// chartValues is a values file that switches instrumentation on through the
// service's own chart. Helm combines maps, so annotations the release already
// sets are kept.
func (s Service) chartValues() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Switches on OpenTelemetry auto-instrumentation (%s) for %s.\n# Pass it after the release's usual values files.\n", languages[s.language()], s.Name)
	for _, key := range s.chartKeys() {
		parts := strings.Split(key, ".")
		for i, p := range parts {
			fmt.Fprintf(&b, "%s%s:\n", strings.Repeat("  ", i), p)
		}
		for _, a := range s.annotations() {
			fmt.Fprintf(&b, "%s%s: %q\n", strings.Repeat("  ", len(parts)), a[0], a[1])
		}
	}
	return b.String()
}
