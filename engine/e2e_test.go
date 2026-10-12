package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLinuxEndToEnd runs the generated scripts on a real systemd host in a
// container, with a real collector, and feeds their real output through the
// journey. It needs podman and a Linux collector package:
//
//	OTELGENIE_E2E_RPM=/path/to/otelcol-contrib_linux_<arch>.rpm go test ./engine -run LinuxEndToEnd -v
func TestLinuxEndToEnd(t *testing.T) {
	rpm := os.Getenv("OTELGENIE_E2E_RPM")
	if rpm == "" {
		t.Skip("set OTELGENIE_E2E_RPM to run")
	}
	const box = "otelgenie-e2e"
	podman := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("podman", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("podman %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	// Scripts report failure through their exit code and still print a block.
	run := func(script string) string {
		out, _ := exec.Command("podman", "exec", box, "bash", "/root/otelgenie-e2e/"+script).Output()
		return string(out)
	}
	exec.Command("podman", "rm", "-f", box).Run()
	podman("run", "-d", "--name", box, "--systemd=always", "registry.access.redhat.com/ubi9/ubi-init:latest")
	defer exec.Command("podman", "rm", "-f", box).Run()
	for i := 0; ; i++ {
		out, _ := exec.Command("podman", "exec", box, "systemctl", "is-system-running").Output()
		if s := strings.TrimSpace(string(out)); s == "running" || s == "degraded" {
			break
		}
		if i > 60 {
			t.Fatal("systemd did not come up in the container")
		}
		time.Sleep(time.Second)
	}
	podman("cp", rpm, box+":/tmp/collector.rpm")
	podman("exec", box, "rpm", "-i", "/tmp/collector.rpm")

	// Stand-in for the collector endpoint: accepts every OTLP/HTTP post.
	sink := `import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', 0)))
        self.send_response(200); self.send_header('Content-Type', 'application/x-protobuf'); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', 4318), H).serve_forever()
`
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sink.py"), []byte(sink), 0o644)
	podman("cp", filepath.Join(dir, "sink.py"), box+":/root/sink.py")
	podman("exec", "-d", box, "python3", "/root/sink.py")

	j, err := Create(testProfile, starter(t), Inputs{
		Project: "payments", Env: "e2e", Endpoint: "http://127.0.0.1:4318",
		Signals: []string{"metrics", "logs", "events"}, Components: []string{"processes", "ports"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	act := func(a Action) {
		t.Helper()
		if _, err := j.Act(a, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	tg := func() *Target { return j.Targets["e2e"] }
	submit := func(data map[string]any) {
		t.Helper()
		act(Action{Type: "submit", Target: "e2e", Step: tg().Step, Data: data})
	}
	ship := func() {
		t.Helper()
		raw, name, err := j.TargetArchive("e2e")
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, name), raw, 0o644)
		if out, err := exec.Command("tar", "xzf", filepath.Join(dir, name), "-C", dir).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		podman("exec", box, "rm", "-rf", "/root/otelgenie-e2e")
		podman("cp", filepath.Join(dir, "otelgenie-e2e"), box+":/root/otelgenie-e2e")
	}

	act(Action{Type: "add_target", Data: map[string]any{"name": "e2e"}})
	ship()
	discovered := run("discover.sh")
	t.Logf("discover.sh printed:\n%s", discovered)
	submit(map[string]any{"paste": discovered})
	if tg().Step != StepConfirm {
		t.Fatalf("real discover output rejected: %s", tg().LastReject)
	}
	if tg().Facts["systemd"] != "yes" || tg().Facts["collector"] != "0.162.0" || tg().Facts["tcp.127.0.0.1:4318"] != "ok" {
		t.Errorf("discovery misread the server: %v", tg().Facts)
	}
	submit(map[string]any{"components": map[string]any{"processes": true, "ports": true}, "vars": map[string]any{"PORTS_CHECK_TARGET": "127.0.0.1:4318"}})
	submit(map[string]any{"choice": "proceed"})
	ship()

	// First run has nowhere to ask for the token, so it must stop short.
	first := run("apply.sh")
	t.Logf("apply.sh (no token) printed:\n%s", first)
	submit(map[string]any{"paste": first})
	if tg().Step != StepApply || tg().Apply == nil || tg().Apply.Result != "placeholders" {
		t.Fatalf("expected the installer to stop for the missing token, got %+v at %s (%s)", tg().Apply, tg().Step, tg().LastReject)
	}
	if out, _ := exec.Command("podman", "exec", box, "systemctl", "is-active", "otelgenie-otelcol").Output(); strings.TrimSpace(string(out)) == "active" {
		t.Fatal("the service was started although a secret was missing")
	}

	// The token is entered on the server; a second run must keep it.
	podman("exec", box, "sed", "-i", "s/^OTEL_ACCESS_TOKEN=.*/OTEL_ACCESS_TOKEN=e2e-token/", "/etc/otelcol-otelgenie/otelcol.env")
	second := run("apply.sh")
	t.Logf("apply.sh (with token) printed:\n%s", second)
	submit(map[string]any{"paste": second})
	if tg().Step != StepVerify {
		t.Fatalf("install did not succeed: %+v\n%s", tg().Apply, podman("exec", box, "journalctl", "-u", "otelgenie-otelcol", "-n", "30", "--no-pager", "-o", "cat"))
	}
	if perm := strings.TrimSpace(podman("exec", box, "stat", "-c", "%a %U", "/etc/otelcol-otelgenie/otelcol.env")); perm != "600 root" {
		t.Errorf("the env file holding secrets is %s, want 600 root", perm)
	}
	t.Logf("limits: %s", strings.TrimSpace(podman("exec", box, "systemctl", "show", "otelgenie-otelcol", "-p", "MemoryMax", "-p", "CPUQuotaPerSecUSec", "-p", "User")))

	var verified string
	for i := 0; i < 10 && tg().Step != StepDone; i++ {
		time.Sleep(20 * time.Second)
		verified = run("verify.sh")
		submit(map[string]any{"paste": verified})
	}
	t.Logf("verify.sh printed:\n%s", verified)
	for _, row := range tg().Verify.Rows {
		t.Logf("  %-28s %-9s %-16s %d %s", row.Name, row.Signal, row.Status, row.Count, row.Note)
	}
	if tg().Step != StepDone {
		t.Fatalf("data never flowed: %v\n%s", tg().Verify.Problems, podman("exec", box, "journalctl", "-u", "otelgenie-otelcol", "-n", "30", "--no-pager", "-o", "cat"))
	}

	// A hand edit on the server is caught by the next check.
	podman("exec", box, "sed", "-i", "s/collection_interval: 30s/collection_interval: 31s/", "/etc/otelcol-otelgenie/conf.d/10-host-metrics.yaml")
	act(Action{Type: "redo", Target: "e2e", Step: StepVerify})
	submit(map[string]any{"paste": run("verify.sh")})
	if problems := strings.Join(tg().Verify.Problems, "\n"); !strings.Contains(problems, "10-host-metrics.yaml on the server is not the file that was generated") {
		t.Errorf("hand edit not noticed: %s", problems)
	}

	rolled := run("rollback.sh")
	left, _ := exec.Command("podman", "exec", box, "bash", "-c", "ls /etc/otelcol-otelgenie /etc/systemd/system/otelgenie-otelcol.service 2>/dev/null; systemctl is-active otelgenie-otelcol").Output()
	if !strings.Contains(rolled, "result=ok") || strings.TrimSpace(string(left)) != "inactive" {
		t.Errorf("rollback left something behind:\n%s\n%s", rolled, left)
	}

	rep := Analyze(&Bundle{Meta: BundleMeta{SessionCode: SessionCode(j.Events)}, Profile: j.Profile, Snap: j.Snap, Events: j.Events, Blobs: j.Blobs}, nil, "")
	if !rep.Rerun.Same {
		t.Errorf("a journey built from real script output does not replay identically: %v", rep.Rerun.Diffs)
	}
	fmt.Println("verdict:", rep.Verdict)
}
