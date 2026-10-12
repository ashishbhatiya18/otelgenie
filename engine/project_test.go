package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

const ordersSpec = `{
  "openapi": "3.0.3",
  "info": {"title": "Orders", "version": "1.2.0"},
  "components": {
    "parameters": {"Tenant": {"name": "X-Tenant-Id", "in": "header", "schema": {"type": "string"}}},
    "securitySchemes": {"key": {"type": "apiKey", "in": "header", "name": "X-Partner-Key"}}
  },
  "paths": {
    "/orders": {
      "parameters": [{"$ref": "#/components/parameters/Tenant"}],
      "get": {
        "parameters": [
          {"name": "X-Channel", "in": "header", "schema": {"type": "string", "enum": ["web", "mobile"]}},
          {"name": "X-Request-Id", "in": "header", "schema": {"type": "string"}},
          {"name": "Authorization", "in": "header", "schema": {"type": "string"}},
          {"name": "limit", "in": "query", "schema": {"type": "integer"}}
        ],
        "responses": {"200": {"description": "ok", "headers": {"X-RateLimit-Remaining": {"schema": {"type": "integer"}}}}}
      },
      "post": {"responses": {"201": {"description": "created"}}}
    }
  }
}`

func TestHeadersAreReadFromAnAPIDescription(t *testing.T) {
	rep, err := AnalyzeSpec(ordersSpec)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Title != "Orders" || rep.Operations != 2 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	kind := map[string]string{}
	for _, h := range rep.Headers {
		kind[h.In+" "+h.Name] = h.Kind
	}
	want := map[string]string{
		"request X-Tenant-Id": HeaderBreakdown, "request X-Channel": HeaderBreakdown, "request X-Request-Id": HeaderUnique,
		"request Authorization": HeaderSecret, "request X-Partner-Key": HeaderSecret, "response X-RateLimit-Remaining": HeaderValue,
	}
	for k, v := range want {
		if kind[k] != v {
			t.Errorf("%s: got %q, want %q", k, kind[k], v)
		}
	}
	if len(kind) != len(want) {
		t.Errorf("unexpected headers: %v", kind)
	}

	old := `{"swagger":"2.0","parameters":{"T":{"name":"X-Region","in":"header","type":"string"}},
	  "paths":{"/a":{"get":{"parameters":[{"$ref":"#/parameters/T"}],"responses":{"200":{"description":"ok","headers":{"Retry-After":{"type":"integer"}}}}}}}}`
	rep, err = AnalyzeSpec(old)
	if err != nil || len(rep.Headers) != 2 || rep.Headers[0].Name != "X-Region" || rep.Headers[1].Kind != HeaderValue {
		t.Fatalf("Swagger 2 was not read: %v %+v", err, rep)
	}
	for _, bad := range []string{"openapi: 3.0.0", `{"name":"not an api"}`} {
		if _, err := AnalyzeSpec(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestProjectPackageHoldsEverything(t *testing.T) {
	in := Inputs{
		Project: "Payments", Env: "prod", Endpoint: "https://gw.internal:4318", Ingress: "https://otel.apps.internal/",
		Signals: []string{"metrics", "logs", "traces"}, Components: []string{"app", "gke", "postgres", "reactjs"},
		Services: []Service{{Name: "orders", Headers: []ServiceHeader{
			{Name: "X-Tenant-Id", In: "request", Kind: HeaderBreakdown},
			{Name: "X-RateLimit-Remaining", In: "response", Kind: HeaderValue},
		}}, {Name: " "}},
	}
	p, err := BuildProject(testProfile, starter(t), in, day0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	gz, err := gzip.NewReader(bytes.NewReader(p.Archive))
	if err != nil {
		t.Fatal(err)
	}
	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		files[strings.TrimPrefix(h.Name, "otelgenie-payments-prod/")] = string(body)
	}
	for _, name := range []string{"README.md", "cluster-gke/apply.sh", "cluster-gke/manifests/20-collector-node.yaml", "cluster-gke/manifests/20-collector-cluster.yaml",
		"server/apply.sh", "server/conf.d/10-postgres-metrics.yaml", "server/conf.d/10-app-metrics.yaml", "guides/reactjs.md"} {
		if files[name] == "" {
			t.Errorf("the package has no %s", name)
		}
	}
	if p.Name != "otelgenie-payments-prod.tar.gz" || files["README.md"] != p.Readme {
		t.Errorf("name or README mismatch: %s", p.Name)
	}
	dims := "OTELGENIE_HEADER_DIMENSIONS=[{name: http.request.header.x-tenant-id}, {name: http.request.header.x_tenant_id}]"
	for _, env := range []string{"cluster-gke/otelcol.env", "server/otelcol.env"} {
		if !strings.Contains(files[env], dims) {
			t.Errorf("%s does not carry the header list:\n%s", env, files[env])
		}
	}
	// Servers go through the Ingress to the cluster's collector, node
	// collectors hand over to it, and only it sends to the destination.
	for name, want := range map[string]string{
		"server/otelcol.env":                              "OTEL_GATEWAY_ENDPOINT=https://otel.apps.internal\n",
		"cluster-gke/otelcol.env":                         "OTEL_GATEWAY_ENDPOINT=https://gw.internal:4318\n",
		"cluster-gke/manifests/10-gke-ingress.yaml":       "name: otelgenie-cluster-collector",
		"cluster-gke/manifests/20-collector-node.yaml":    "endpoint: http://otelgenie-cluster-collector.observability.svc:4318",
		"cluster-gke/manifests/20-collector-cluster.yaml": "otlp/gateway",
		"cluster-gke/settings.env":                        "NAMESPACE=observability\n",
		"server/settings.env":                             "SERVICE_USER=observability\n",
	} {
		if !strings.Contains(files[name], want) {
			t.Errorf("%s does not have %q:\n%s", name, want, files[name])
		}
	}
	if strings.Contains(files["cluster-gke/manifests/20-collector-cluster.yaml"], "otelgenie-cluster-collector.observability.svc") {
		t.Error("the cluster's collector sends to itself")
	}
	// Until the Ingress exists its address is not known: the process asks for
	// it after the step that creates it, and the server's files wait for it.
	in.Ingress = ""
	early, err := BuildProject(testProfile, starter(t), in, day0)
	if err != nil {
		t.Fatal(err)
	}
	asked, waited := -1, -1
	for i, s := range early.Process {
		if s.Ask == "ingress" {
			asked = i
		}
		if s.Waiting {
			waited = i
		}
	}
	if asked < 0 || waited <= asked || strings.Contains(early.Readme, "`server/`") {
		t.Errorf("without the address: asked at %d, waiting at %d", asked, waited)
	}
	// The connectivity check runs from the image named in the setup.
	internal := testProfile
	internal.CurlImage = " quay.internal/tools/curl:8 "
	if own, err := BuildProject(internal, starter(t), in, day0); err != nil || !strings.Contains(own.Readme, "--image=quay.internal/tools/curl:8 --command") || !strings.Contains(early.Readme, "--image=curlimages/curl --command") {
		t.Errorf("the check does not use the setup's curl image: %v", err)
	}
	in.Components = []string{"app", "gke"}
	if alone, err := BuildProject(testProfile, starter(t), in, day0); err != nil || strings.Contains(alone.Readme, "Create the Ingress") || strings.Contains(alone.Readme, "bash otelgenie.sh check-ingress\n") {
		t.Errorf("a cluster on its own should have no Ingress: %v", err)
	}

	// The checking script travels with what it needs to know about the project.
	for _, want := range []string{"DESTINATION='https://gw.internal:4318'", "SEND_TO='https://otel.apps.internal'", "NAMESPACE='observability'", "CLUSTER_FOLDERS='cluster-gke'", "SERVER_FOLDER='server'", "EXPECT_INGRESS='yes'", "EXPECT_INSTRUMENTATION='yes'"} {
		if !strings.Contains(files["project.env"], want+"\n") {
			t.Errorf("project.env does not have %s:\n%s", want, files["project.env"])
		}
	}
	if !strings.HasPrefix(files["otelgenie.sh"], "#!/usr/bin/env bash") || !strings.Contains(files["prerequisites.txt"], "cert-manager\tkubectl get crd certificates.cert-manager.io") || p.Process[len(p.Process)-2].Title != "Validate everything on Kubernetes" {
		t.Error("the checking script, its prerequisites or the final validation step is missing")
	}
	// Every command of the guide goes through the script, apart from getting
	// the package onto the jump server and upgrading a Helm release.
	for _, step := range p.Process {
		for _, c := range step.Commands {
			if !strings.Contains(c.Text, "otelgenie.sh ") && !strings.HasPrefix(c.Text, "scp ") && !strings.HasPrefix(c.Text, "tar ") && !strings.HasPrefix(c.Text, "helm ") && !strings.HasPrefix(c.Text, "annotations:") && !strings.HasPrefix(c.Text, "OTEL_") {
				t.Errorf("%q runs something directly: %s", step.Title, c.Text)
			}
		}
	}
	// On a server the script installs the collector package too.
	if !strings.Contains(files["packages.txt"], "rhel\tx86_64\totelcol-contrib_0.162.0_linux_amd64.rpm\thttps://") || !strings.Contains(files["project.env"], "COLLECTOR_VERSION='0.162.0'\n") || !strings.Contains(p.Readme, "sudo bash otelgenie.sh install-collector\n") || strings.Contains(p.Readme, "check-after-install") {
		t.Errorf("the server install does not carry what it needs:\n%s", files["packages.txt"])
	}
	// Every script command can also be done by hand, and nothing else offers that.
	for _, step := range p.Process {
		for _, c := range step.Commands {
			if scripted, manual := strings.Contains(c.Text, "otelgenie.sh "), step.Manual[c.Text]; scripted != (manual != "") || strings.Contains(manual, "otelgenie.sh") {
				t.Errorf("%q, %s: by hand is %q", step.Title, c.Text, manual)
			}
		}
	}
	if !strings.Contains(p.Readme, "The same by hand, without otelgenie.sh") || !strings.Contains(p.Readme, "kubectl apply -f cert-manager.yaml") || !strings.Contains(p.Readme, "kubectl apply --server-side -f cluster-gke/opentelemetry-operator.yaml") {
		t.Error("the README does not show how to do the script's work by hand")
	}
	// One command checks the jump server and then the images.
	if second := p.Process[1]; len(second.Commands) != 1 || second.Commands[0].Text != "bash otelgenie.sh check-requirements" || strings.Contains(p.Readme, "check-images") {
		t.Errorf("the second step does not check the jump server and the images together: %+v", second.Commands)
	}
	// The script reports on the prerequisites, then installs what is missing.
	// On GKE the metrics server comes with the cluster, so it is not offered.
	look, put := strings.Index(p.Readme, "bash otelgenie.sh check-prerequisites\n"), strings.Index(p.Readme, "bash otelgenie.sh install-prerequisites\n")
	if look < 0 || put < look || strings.Contains(p.Readme, "metrics-server.yaml") || !strings.Contains(p.Readme, "the metrics server: comes with gke") {
		t.Errorf("the prerequisites step is wrong for GKE: check at %d, install at %d", look, put)
	}
	if want := "the metrics server\tkubectl get apiservice v1beta1.metrics.k8s.io && kubectl top nodes\tmetrics-server.yaml\thttps://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml\t-\tgke,ocp\n"; !strings.Contains(files["prerequisites.txt"], want) {
		t.Errorf("prerequisites.txt does not say where the metrics server is built in:\n%s", files["prerequisites.txt"])
	}
	ocp := in
	ocp.Components = []string{"ocp"}
	if shift, err := BuildProject(testProfile, starter(t), ocp, day0); err != nil || !strings.Contains(shift.Readme, "Check the cluster's prerequisites") || strings.Contains(shift.Readme, "install-prerequisites\n") {
		t.Errorf("OpenShift should have nothing to install: %v", err)
	}
	inst := files["cluster-gke/manifests/10-app-instrumentation.yaml"]
	if !strings.Contains(inst, `value: "X-Tenant-Id"`) || !strings.Contains(inst, `value: "X-RateLimit-Remaining"`) || strings.Contains(inst, "${env:") {
		t.Errorf("the Instrumentation resource does not name the headers to capture:\n%s", inst)
	}
	if !strings.Contains(files["cluster-gke/manifests/20-collector-node.yaml"], "spanmetrics/app") {
		t.Error("the node collector does not turn traces into metrics")
	}
	if g := files["guides/reactjs.md"]; strings.Contains(g, "#! ") || strings.Contains(g, "${env:") || !strings.Contains(g, "'service.namespace': 'Payments'") {
		t.Errorf("the guide was not filled in:\n%s", g)
	}
	var titles []string
	for _, s := range p.Process {
		titles = append(titles, s.Title)
	}
	order := strings.Join(titles, " > ")
	for _, want := range []string{"Download the package and move it to the jump server > Check the jump server is ready and every container image can be pulled > Check the destination can be reached > Install kube-state-metrics and cert-manager > Install the OpenTelemetry Operator > Validate the operator > Install the collectors", "Validate that data is flowing", "Instrument the microservices", "Instrument the frontend"} {
		if !strings.Contains(order, want) {
			t.Errorf("the process is missing %q: %s", want, order)
		}
	}
	for _, want := range []string{"https://gw.internal:4318/v1/traces", "opentelemetry-operator.yaml", "| X-Tenant-Id | request |", "### orders"} {
		if !strings.Contains(p.Readme, want) {
			t.Errorf("README is missing %q", want)
		}
	}

	in.Components = []string{"app", "gke"}
	p, err = BuildProject(testProfile, starter(t), in, day0)
	if err != nil || strings.Contains(p.Readme, "`server/`") || !strings.Contains(p.Readme, "`cluster-gke/`") {
		t.Fatalf("a project that runs entirely in a cluster should not get server files: %v", err)
	}

	// A project with no cluster and nothing chosen still gets the server files.
	in.Components, in.Services = nil, nil
	p, err = BuildProject(testProfile, starter(t), in, day0)
	if err != nil || strings.Contains(p.Readme, "Operator") || !strings.Contains(p.Readme, "`server/`") {
		t.Fatalf("server-only project: %v\n%s", err, p.Readme)
	}
}
