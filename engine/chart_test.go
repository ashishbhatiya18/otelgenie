package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

var testChart = map[string]string{
	"orders/Chart.yaml":  "apiVersion: v2\nname: orders\nversion: 1.4.0\n",
	"orders/values.yaml": "podAnnotations: {}\n",
	"orders/templates/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "orders.fullname" . }}
  {{- with .Values.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  template:
    metadata:
      {{- with .Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
`,
	"orders/templates/service.yaml":              "apiVersion: v1\nkind: Service\n",
	"orders/charts/redis/Chart.yaml":             "name: redis\nversion: 9.0.0\n",
	"orders/charts/redis/templates/workers.yaml": "kind: StatefulSet\n",
}

func TestChartShowsWhereThePodAnnotationGoes(t *testing.T) {
	rep, err := AnalyzeChart(testChart)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Name != "orders" || rep.Version != "1.4.0" || rep.Injected {
		t.Errorf("wrong chart: %+v", rep)
	}
	// The workload's own annotations and the chart packed inside are not it.
	if len(rep.Workloads) != 1 || rep.Workloads[0] != (ChartWorkload{Kind: "Deployment", File: "templates/deployment.yaml", Key: "podAnnotations"}) {
		t.Errorf("wrong workloads: %+v", rep.Workloads)
	}

	fixed := map[string]string{"Chart.yaml": "name: billing\n", "templates/sts.yaml": "kind: StatefulSet\nspec:\n  template:\n    metadata:\n      labels: {}\n"}
	if rep, err = AnalyzeChart(fixed); err != nil || len(rep.Workloads) != 1 || rep.Workloads[0].Key != "" {
		t.Errorf("a template without a value for annotations: %+v, %v", rep, err)
	}
	if _, err = AnalyzeChart(map[string]string{"values.yaml": "a: 1\n"}); err == nil {
		t.Error("a folder without Chart.yaml was taken for a chart")
	}
}

func TestPackagedChartIsUnpacked(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range testChart {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	files, err := UnpackChart(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := AnalyzeChart(files); err != nil || rep.Name != "orders" || len(rep.Workloads) != 1 {
		t.Errorf("wrong chart from the package: %+v, %v", rep, err)
	}
	if _, err := UnpackChart([]byte("not a chart")); err == nil {
		t.Error("something that is not a package was accepted")
	}
}

func TestPackageSetsTheAnnotationThroughEachChart(t *testing.T) {
	in := Inputs{
		Project: "Payments", Env: "prod", Endpoint: "https://gw.internal:4318",
		Signals: []string{"traces"}, Components: []string{"app", "gke"},
		Stacks: []string{"python", "go"},
		Services: []Service{
			{Name: "orders", Language: "python", Chart: &ServiceChart{Name: "orders", Workloads: []ChartWorkload{{Kind: "Deployment", File: "templates/deployment.yaml", Key: "app.podAnnotations"}}}},
			{Name: "billing", Chart: &ServiceChart{Name: "billing", Workloads: []ChartWorkload{{Kind: "StatefulSet", File: "templates/sts.yaml"}}}},
			{Name: "ledger", Language: "python"},
			{Name: "router", Language: "go"},
		},
	}
	p, err := BuildProject(testProfile, starter(t), in, day0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	gz, _ := gzip.NewReader(bytes.NewReader(p.Archive))
	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(tr)
		files[strings.TrimPrefix(h.Name, "otelgenie-payments-prod/")] = string(body)
	}
	want := "app:\n  podAnnotations:\n    instrumentation.opentelemetry.io/inject-python: \"observability/otelgenie\"\n"
	if got := files["services/orders/values-otel.yaml"]; !strings.HasSuffix(got, want) {
		t.Errorf("wrong values file for orders:\n%s", got)
	}
	if files["services/billing/values-otel.yaml"] != "" || files["services/ledger/values-otel.yaml"] != "" {
		t.Error("a values file was written for a service whose chart cannot use one")
	}
	// Only the agents for the chosen stacks are asked for, and a Go service
	// also has to name its binary.
	if list := files["cluster-gke/images.txt"]; strings.Contains(list, "autoinstrumentation-java") || !strings.Contains(list, "autoinstrumentation-python") || !strings.Contains(list, "autoinstrumentation-go") {
		t.Errorf("images.txt does not follow the chosen stacks:\n%s", list)
	}
	if !strings.Contains(files["project.env"], "APPLICATION_STACKS='go python'\n") || !strings.Contains(p.Readme, "stacks you chose (Go, Python) and no others") {
		t.Errorf("the image check is not told the stacks:\n%s", files["project.env"])
	}
	bare := in
	bare.Components, bare.Services = []string{"gke"}, nil
	if none, err := BuildProject(testProfile, starter(t), bare, day0); err != nil || strings.Contains(none.Readme, "autoinstrumentation-") {
		t.Errorf("a project without applications still asks for agent images: %v", err)
	}
	if !strings.Contains(p.Readme, "bash otelgenie.sh instrument-workload YOUR-NAMESPACE deployment/router go /PATH/TO/THE/BINARY") {
		t.Error("the Go service is not told to name its binary")
	}
	for _, want := range []string{
		"helm upgrade YOUR-RELEASE PATH-TO-CHART -n YOUR-NAMESPACE --reuse-values -f services/orders/values-otel.yaml",
		"In templates/sts.yaml of the billing chart",
		"instrumentation.opentelemetry.io/inject-java: \"observability/otelgenie\"",
		"bash otelgenie.sh instrument-workload YOUR-NAMESPACE deployment/ledger python",
	} {
		if !strings.Contains(p.Readme, want) {
			t.Errorf("the README does not have: %s", want)
		}
	}
}
