package engine

import (
	"strings"
	"testing"
	"time"
)

func TestClusterHappyPath(t *testing.T) {
	profile := testProfile
	profile.ImageMethod, profile.Registry = "mirror", "https://registry.internal/mirror/"
	j, err := Create(profile, starter(t), Inputs{
		Project: "payments", Env: "prod", Endpoint: "https://gw.internal:4318",
		Signals: []string{"metrics", "logs", "events", "traces"}, Components: []string{"app", "eks", "gke", "postgres"},
	}, day0)
	if err != nil {
		t.Fatal(err)
	}
	now := day0
	tg := func() *Target { return j.Targets["prod-gke-1"] }
	act := func(a Action) {
		t.Helper()
		now = now.Add(time.Minute)
		if _, err := j.Act(a, now); err != nil {
			t.Fatal(err)
		}
	}
	submit := func(data map[string]any) {
		t.Helper()
		act(Action{Type: "submit", Target: "prod-gke-1", Step: tg().Step, Data: data})
	}
	file := func(path string) string { return j.Blobs[tg().Files[path]] }

	act(Action{Type: "add_target", Data: map[string]any{"name": "prod-gke-1", "kind": "k8s"}})
	if got := file("discover.items"); got != "svc kube-state-metrics\nsvc metrics-server\n" {
		t.Fatalf("discover.items for a cluster: %q", got)
	}
	if !strings.Contains(file("discover.sh"), "kubectl") {
		t.Fatal("a cluster should get the kubectl discover script")
	}

	submit(map[string]any{"paste": block("DISCOVER",
		"time=1791700000", "cli=kubectl", "host=gke_prod", "cluster=reachable", "server_version=v1.31.4-gke.1183000", "nodes=3", "arch=amd64",
		"distro=gke", "admin=yes", "operator=none", "cert_manager=yes", "svc.kube-state-metrics=monitoring:8080", "svc.metrics-server=kube-system:443")})
	found := map[string]bool{}
	for _, it := range j.State().Views[0].Form.Items {
		found[it.Component] = it.Found
	}
	if len(found) != 3 || !found["gke"] || found["eks"] || !found["app"] {
		t.Fatalf("gke was reported, eks was not, app cannot be checked, postgres does not run in a cluster: %v", found)
	}

	submit(map[string]any{"components": map[string]any{"gke": true, "eks": true, "app": true}})
	if tg().Step != StepConfirm || !strings.Contains(tg().LastReject, "exactly one kind of cluster") {
		t.Fatalf("two kinds of cluster were accepted: %s %q", tg().Step, tg().LastReject)
	}
	submit(map[string]any{"components": map[string]any{"gke": true, "app": true}})
	if tg().Step != StepReview {
		t.Fatalf("confirm did not pass: %q", tg().LastReject)
	}

	node, cluster := file("manifests/20-collector-node.yaml"), file("manifests/20-collector-cluster.yaml")
	docs, err := yamlDocs(node)
	if err != nil || len(docs) != 1 {
		t.Fatalf("node collector is not one YAML document: %v\n%s", err, node)
	}
	spec := docs[0]["spec"].(map[string]any)
	cfg := spec["config"].(map[string]any)
	pipelines := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
	for _, p := range []string{"metrics/gke_node", "logs/gke", "traces/app", "metrics/app", "logs/app"} {
		if pipelines[p] == nil {
			t.Errorf("node collector has no pipeline %s", p)
		}
	}
	if pipelines["metrics/gke_cluster"] != nil || strings.Contains(node, "eks") {
		t.Error("the node collector holds configuration that is not its own")
	}
	if got := cfg["processors"].(map[string]any)["resourcedetection"].(map[string]any)["detectors"]; len(got.([]any)) != 2 {
		t.Errorf("the platform's detectors should replace the base list, got %v", got)
	}
	if spec["mode"] != "daemonset" || !strings.Contains(node, "fieldPath: status.hostIP") || !strings.Contains(node, "fieldPath: spec.nodeName") {
		t.Errorf("node collector lost its mode or its pod-provided variables:\n%s", node)
	}
	for _, want := range []string{"${env:KSM_ENDPOINT:-kube-state-metrics.kube-system.svc:8080}", "logs/gke_events", "mode: deployment"} {
		if !strings.Contains(cluster, want) {
			t.Errorf("cluster collector is missing %q", want)
		}
	}
	env := file("otelcol.env")
	for _, want := range []string{"KSM_ENDPOINT=kube-state-metrics.monitoring.svc:8080", "METRICS_SERVER_ENDPOINT=metrics-server.kube-system.svc:443", "OTELGENIE_CLUSTER=prod-gke-1", "OTEL_ACCESS_TOKEN=CHANGE_ME"} {
		if !strings.Contains(env, want) {
			t.Errorf("otelcol.env is missing %s:\n%s", want, env)
		}
	}
	if strings.Contains(env, "K8S_NODE") {
		t.Errorf("variables the pod provides do not belong in otelcol.env:\n%s", env)
	}
	if strings.Contains(node, "ghcr.io") || !strings.Contains(node, "image: registry.internal/mirror/open-telemetry/") {
		t.Errorf("images were not pointed at the internal registry:\n%s", node)
	}
	if inst := file("manifests/10-app-instrumentation.yaml"); !strings.Contains(inst, "kind: Instrumentation") || strings.Contains(inst, "ghcr.io") {
		t.Errorf("the Instrumentation resource is missing or still pulls from outside:\n%s", inst)
	}
	if !strings.Contains(file("manifests/10-gke-rbac.yaml"), "kind: ClusterRole") || !strings.Contains(file("settings.env"), "PLATFORM=gke\nCOLLECTORS=\"node cluster\"") {
		t.Errorf("rbac or settings are wrong:\n%s", file("settings.env"))
	}

	submit(map[string]any{"choice": "proceed"})
	if text := j.State().Views[0].Text(); !strings.Contains(text, "opentelemetry-operator.yaml") {
		t.Errorf("the install step does not say how to get the operator:\n%s", text)
	}
	submit(map[string]any{"paste": block("APPLY", "time=1", "host=gke_prod", "result=missing-operator")})
	if tg().Step != StepApply {
		t.Fatal("a failed install moved on")
	}
	submit(map[string]any{"paste": block("APPLY", "time=1", "host=gke_prod", "operator=installed-now", "placeholders=0", "service=active", "result=ok")})

	lines := []string{"time=2", "host=gke_prod", "service=active", "restarts=0", "placeholders=0", "metrics=ok"}
	for _, p := range sortedKeys(tg().Files) {
		if strings.HasPrefix(p, "manifests/") {
			lines = append(lines, "file."+p+"="+tg().Files[p])
		}
	}
	flowing := append(lines,
		"m.otelcol_receiver_accepted_metric_points|kubeletstats/gke=900",
		"m.otelcol_receiver_accepted_metric_points|prometheus/gke_ksm=400",
		"m.otelcol_receiver_accepted_metric_points|prometheus/gke_metrics_server=12",
		"m.otelcol_exporter_sent_metric_points|otlphttp=1300")
	submit(map[string]any{"paste": block("VERIFY", append(append([]string{}, lines[:2]...), append([]string{"service=node 2 of 3 ready; cluster 1 of 1 ready; "}, flowing[3:]...)...)...)})
	if tg().Step != StepVerify || !strings.Contains(strings.Join(tg().Verify.Problems, "\n"), "Not every collector pod is running") {
		t.Fatalf("an unready collector passed verify: %+v", tg().Verify)
	}
	submit(map[string]any{"paste": block("VERIFY", flowing...)})
	if tg().Step != StepDone {
		t.Fatalf("healthy verify did not finish: %+v", tg().Verify)
	}
	if text := j.State().Views[0].Text(); strings.Count(text, "inject-java") != 1 {
		t.Errorf("the hand-out to application owners should appear once:\n%s", text)
	}

	// The whole thing is rebuilt from the log and replays identically.
	again, err := Open(profile, starter(t), j.Events, j.Blobs)
	if err != nil || again.Targets["prod-gke-1"].Step != StepDone {
		t.Fatalf("reopening lost the cluster: %v", err)
	}
	raw, _, err := j.ExportBundle(now, "test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res := Rerun(b); !res.Same {
		t.Fatalf("replay differs: %+v", res)
	}
}

func TestClusterNeedsAKindOfCluster(t *testing.T) {
	r := newRun(t)
	r.act(Action{Type: "add_target", Data: map[string]any{"name": "some-cluster", "kind": "k8s"}})
	if r.j.Targets["some-cluster"] != nil {
		t.Fatal("a cluster was added to a project with no kind of cluster among its components")
	}
}
