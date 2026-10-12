package engine

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func starter(t *testing.T) *Snapshot {
	t.Helper()
	root := filepath.Join("..", "template-starter")
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return SnapshotFromFiles("acme/otel-templates", strings.Repeat("a", 40), files)
}

var testProfile = Profile{
	TemplateRepo: "https://github.com/Acme/otel-templates.git", PackageMethod: "manual",
	ImageMethod: "none", OSFamilies: []string{"rhel"},
}

var day0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func block(kind string, lines ...string) string {
	return fmt.Sprintf("OTELGENIE-%s v1\n%s\nEND OTELGENIE-%s lines=%d\n", kind, strings.Join(lines, "\n"), kind, len(lines))
}

var discoverLines = []string{
	"host=db-host-1", "time=1791700000", "os_id=rhel", "os_like=fedora", "os_version=9.4", "arch=x86_64",
	"mem_total_kb=16000000", "cpus=8", "systemd=yes", "collector=none", "journal=ok", "ports_source=ss",
	"port.5432=open", "port.9187=closed", "unit.postgresql=active", "glob./var/lib/pgsql/data/log/*.log=3",
	"glob./var/log/postgresql/*.log=0", "tcp.gw.internal:4318=ok",
}

type run struct {
	t   *testing.T
	j   *Journey
	now time.Time
}

func (r *run) act(a Action) {
	r.t.Helper()
	r.now = r.now.Add(time.Minute)
	if _, err := r.j.Act(a, r.now); err != nil {
		r.t.Fatal(err)
	}
}

func (r *run) submit(data map[string]any) {
	r.t.Helper()
	tg := r.j.Targets["db-host-1"]
	r.act(Action{Type: "submit", Target: tg.Name, Step: tg.Step, Data: data})
}

func (r *run) step() string { return r.j.Targets["db-host-1"].Step }

func newRun(t *testing.T) *run {
	t.Helper()
	j, err := Create(testProfile, starter(t), Inputs{
		Project: "payments", Env: "prod", Endpoint: "https://gw.internal:4318",
		Signals: []string{"metrics", "logs", "traces"}, Components: []string{"postgres", "app"},
	}, day0)
	if err != nil {
		t.Fatal(err)
	}
	r := &run{t: t, j: j, now: day0}
	r.act(Action{Type: "add_target", Data: map[string]any{"name": "db-host-1"}})
	return r
}

func (r *run) verifyBlock(extra ...string) string {
	lines := []string{"host=db-host-1", "time=1791700900", "service=active", "restarts=0", "placeholders=0", "metrics=ok"}
	tg := r.j.Targets["db-host-1"]
	for _, path := range sortedKeys(tg.Files) {
		if strings.HasPrefix(path, "conf.d/") {
			lines = append(lines, "file."+path+"="+tg.Files[path])
		}
	}
	return block("VERIFY", append(lines, extra...)...)
}

var healthy = []string{
	"m.otelcol_scraper_scraped_metric_points|hostmetrics/host=66",
	"m.otelcol_receiver_accepted_metric_points|prometheus/postgres=412",
	"m.otelcol_receiver_accepted_log_records|journald/host=9",
	"m.otelcol_exporter_sent_metric_points|otlphttp=478",
}

// toVerify walks the journey to the verify step the way a user would.
func (r *run) toVerify() {
	r.t.Helper()
	r.submit(map[string]any{"paste": block("DISCOVER", discoverLines...)})
	r.submit(map[string]any{"components": map[string]any{"postgres": true, "app": true}, "vars": map[string]any{}})
	r.submit(map[string]any{"choice": "proceed"})
	r.submit(map[string]any{"paste": block("APPLY", "host=db-host-1", "collector=0.162.0", "placeholders=0", "service=active", "result=ok")})
	if r.step() != StepVerify {
		r.t.Fatalf("expected to reach verify, at %s (%s)", r.step(), r.j.Targets["db-host-1"].LastReject)
	}
}

func TestStarterTemplateIsClean(t *testing.T) {
	_, problems := LoadCatalog(starter(t).Files)
	if len(problems) > 0 {
		t.Fatalf("starter template has lint problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestLintCatchesMistakes(t *testing.T) {
	bad := `#! otelgenie component=redis signal=metrics targets=vm title="Redis"
#! discover port=6379 -> REDIS_ENDPOINT as=localhost:{port}
receivers:
  redis:
    endpoint: ${env:REDIS_HOST}
service:
  pipelines:
    metrics:
      receivers: [redis]
`
	_, problems := ParseEntry("catalog/redis/metrics.yaml", bad)
	want := []string{"REDIS_HOST} is used but", "REDIS_ENDPOINT is declared", `receiver "redis" must be named`, `pipeline "metrics" must be named`}
	for _, w := range want {
		if !strings.Contains(strings.Join(problems, "\n"), w) {
			t.Errorf("lint did not report %q; got:\n%s", w, strings.Join(problems, "\n"))
		}
	}
}

func TestFingerprintIgnoresSpellingAndLocatesDifferences(t *testing.T) {
	snap := starter(t)
	man, _ := snap.Manifest()
	a := Profile{PackageMethod: "repo", PackageRepoURL: "https://Repo.Internal/otel/", OSFamilies: []string{"rhel", "debian"}, CABundle: "PEM\r\n"}
	b := Profile{PackageMethod: "repo", PackageRepoURL: "https://repo.internal/otel", OSFamilies: []string{"Debian", "rhel"}, CABundle: "PEM\n"}
	fa, fb := ComputeFingerprint(a, snap, man), ComputeFingerprint(b, snap, man)
	if fa.Code != fb.Code {
		t.Fatalf("equivalent setups got different codes: %s vs %s", fa, fb)
	}
	b.CABundle = "OTHER"
	fc := ComputeFingerprint(b, snap, man)
	if diff := DiffSegments(fa.Segments, fc.Segments); len(diff) != 1 || diff[0] != "ca-bundle" {
		t.Fatalf("expected only ca-bundle to differ, got %v", diff)
	}
	if parsed := ParseFingerprint(strings.ToLower(fa.String())); parsed.Code != fa.Code || len(DiffSegments(parsed.Segments, fa.Segments)) != 0 {
		t.Fatalf("a pasted fingerprint did not round-trip: %+v", parsed)
	}
}

func TestRedaction(t *testing.T) {
	in := "Authorization: Bearer abcdef1234567890\nPOSTGRES_PASSWORD=hunter2\nurl=postgres://mon:s3cret@db:5432/x\nOTEL_ACCESS_TOKEN=CHANGE_ME\nport.5432=open"
	out := Redact(in)
	for _, secret := range []string{"abcdef1234567890", "hunter2", "s3cret"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q survived redaction:\n%s", secret, out)
		}
	}
	for _, keep := range []string{"OTEL_ACCESS_TOKEN=CHANGE_ME", "port.5432=open", "mon:[REDACTED]@db"} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q should have been kept:\n%s", keep, out)
		}
	}
}

func TestHappyPathGeneratesNativeConfig(t *testing.T) {
	r := newRun(t)
	if got := r.j.Blobs[r.j.Targets["db-host-1"].Files["discover.items"]]; !strings.Contains(got, "port 9187") || !strings.Contains(got, "tcp gw.internal 4318") {
		t.Fatalf("discover.items is missing probes:\n%s", got)
	}
	r.toVerify()
	tg := r.j.Targets["db-host-1"]
	file := func(path string) string { return r.j.Blobs[tg.Files[path]] }

	// Catalog files reach the server byte for byte.
	if file("conf.d/10-postgres-metrics.yaml") != r.j.Snap.Files["catalog/postgres/metrics.yaml"] {
		t.Error("the postgres config was altered on its way to the server")
	}
	env := file("otelcol.env")
	for _, want := range []string{
		"OTEL_GATEWAY_ENDPOINT=https://gw.internal:4318", "OTELGENIE_PROJECT=payments", "OTEL_ACCESS_TOKEN=CHANGE_ME",
		"POSTGRES_LOG_GLOB=/var/lib/pgsql/data/log/*.log", "OTELGENIE_MEMORY_LIMIT_MIB=409",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("otelcol.env lacks %q:\n%s", want, env)
		}
	}
	// The exporter port was closed, so no address is written and a request is produced.
	if strings.Contains(env, "POSTGRES_EXPORTER_ENDPOINT") {
		t.Error("an exporter address was invented although the port was closed")
	}
	if _, ok := tg.Files["requests/postgres-postgres_exporter.md"]; !ok {
		t.Errorf("no request note for the missing exporter; files: %v", sortedKeys(tg.Files))
	}
	if !strings.Contains(file("settings.env"), "MEMORY_MAX_MB=512") {
		t.Errorf("unexpected limits:\n%s", file("settings.env"))
	}
	for _, path := range sortedKeys(tg.Files) {
		if path != "MANIFEST.sha256" && !strings.Contains(file("MANIFEST.sha256"), tg.Files[path]+"  "+path) {
			t.Errorf("%s is not covered by the manifest", path)
		}
	}

	r.submit(map[string]any{"paste": r.verifyBlock(healthy...)})
	if r.step() != StepDone {
		t.Fatalf("healthy verify did not finish: %+v", tg.Verify)
	}
	if text := r.j.State().Views[0].Text(); !strings.Contains(text, "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318") {
		t.Errorf("the traces hand-out is missing from the last screen:\n%s", text)
	}
}

func TestUndetectableComponentsStartTicked(t *testing.T) {
	r := newRun(t)
	r.submit(map[string]any{"paste": block("DISCOVER", discoverLines...)})
	found := map[string]bool{}
	for _, it := range r.j.State().Views[0].Form.Items {
		found[it.Component] = it.Found
	}
	if !found["app"] || !found["postgres"] {
		t.Fatalf("app has nothing to detect and postgres was detected, so both should start ticked: %v", found)
	}
}

func TestBadPastesAreExplained(t *testing.T) {
	r := newRun(t)
	cases := map[string]string{
		"":                                      "nothing was pasted",
		"$ bash discover.sh\nhost=x":            "could not find the line",
		"OTELGENIE-DISCOVER v1\nhost=x\nport.1": "cut off",
		block("APPLY", "result=ok"):             "output of the apply script",
		"OTELGENIE-DISCOVER v1\nhost=x\nEND OTELGENIE-DISCOVER lines=5": "looks incomplete",
	}
	for paste, want := range cases {
		r.submit(map[string]any{"paste": paste})
		if got := r.j.Targets["db-host-1"].LastReject; !strings.Contains(got, want) || r.step() != StepDiscover {
			t.Errorf("paste %q: want rejection containing %q, got %q at %s", paste, want, got, r.step())
		}
	}
}

func TestVerifyNamesTheRealCause(t *testing.T) {
	r := newRun(t)
	r.toVerify()
	scrape := `err.1=warn Failed to scrape Prometheus endpoint {"otelcol.component.id": "prometheus/postgres", "error": "Get \"http://localhost:9187/metrics\": dial tcp [::1]:9187: connect: connection refused"}`
	auth := `err.2=info Exporting failed. {"otelcol.component.id": "otlphttp", "otelcol.component.kind": "exporter", "error": "HTTP Status Code 401, Message=Unauthorized"}`
	// A line about an extension must not be mistaken for a failing exporter.
	noise := `err.3=warn something minor {"otelcol.component.id": "health_check", "otelcol.component.kind": "extension"}`
	r.submit(map[string]any{"paste": r.verifyBlock(
		"m.otelcol_scraper_scraped_metric_points|hostmetrics/host=66",
		"m.otelcol_receiver_accepted_metric_points|prometheus/postgres=10", scrape, auth, noise)})
	if r.step() != StepVerify {
		t.Fatal("a failing verify moved on")
	}
	byName := map[string]VerifyRow{}
	for _, row := range r.j.Targets["db-host-1"].Verify.Rows {
		byName[row.Name] = row
	}
	if pg := byName["prometheus/postgres"]; pg.Status != "failing" || pg.Diagnosis == nil || !strings.Contains(pg.Diagnosis.Cause, "Nothing is listening") {
		t.Errorf("postgres scrape failure not diagnosed: %+v", pg)
	}
	if ex := byName["otlphttp"]; ex.Status != "not-delivering" || ex.Diagnosis == nil || !strings.Contains(ex.Diagnosis.Cause, "rejected the access token") {
		t.Errorf("token rejection not diagnosed: %+v", ex)
	}
	if _, wrong := byName["health_check"]; wrong {
		t.Error("an extension was reported as an exporter")
	}
	if host := byName["hostmetrics/host"]; host.Status != "receiving" {
		t.Errorf("host metrics should be fine: %+v", host)
	}
}

func TestVerifyCatchesHandEditedFiles(t *testing.T) {
	r := newRun(t)
	r.toVerify()
	paste := strings.Replace(r.verifyBlock(healthy...), r.j.Targets["db-host-1"].Files["conf.d/00-base.yaml"], strings.Repeat("0", 64), 1)
	r.submit(map[string]any{"paste": paste})
	if problems := strings.Join(r.j.Targets["db-host-1"].Verify.Problems, "\n"); !strings.Contains(problems, "conf.d/00-base.yaml on the server is not the file that was generated") {
		t.Errorf("drift not reported:\n%s", problems)
	}
}

func TestWaitingDroppingAndStaleDiscovery(t *testing.T) {
	r := newRun(t)
	r.submit(map[string]any{"paste": block("DISCOVER", discoverLines...)})
	r.submit(map[string]any{"components": map[string]any{"postgres": true}, "vars": map[string]any{}})
	r.submit(map[string]any{"choice": "waiting"})
	tg := r.j.Targets["db-host-1"]
	if tg.WaitingSince == "" || r.step() != StepReview {
		t.Fatalf("waiting was not recorded: %+v", tg)
	}
	r.now = r.now.Add(4 * 24 * time.Hour)
	r.act(Action{Type: "resume"})
	if text := r.j.State().Views[0].Text(); !strings.Contains(text, "Waiting since 4 days ago") {
		t.Errorf("the recap does not mention the wait:\n%s", text)
	}
	r.submit(map[string]any{"choice": "drop"})
	if r.step() != StepApply || len(tg.Selected) != 0 || !contains(tg.Deferred, "postgres") {
		t.Fatalf("drop did not set postgres aside: step=%s selected=%v deferred=%v", r.step(), tg.Selected, tg.Deferred)
	}
	if _, still := tg.Files["conf.d/10-postgres-metrics.yaml"]; still {
		t.Error("postgres config is still generated after being set aside")
	}
	if v := r.j.State().Views[0]; v.Form.Kind != "choice" || !strings.Contains(v.Text(), "last looked at 4 days ago") {
		t.Fatalf("stale discovery was not raised:\n%s", v.Text())
	}
	r.submit(map[string]any{"choice": "rediscover"})
	if r.step() != StepDiscover {
		t.Fatalf("rediscover went to %s", r.step())
	}
}

func TestStateSurvivesRestartAndDetectsDamage(t *testing.T) {
	r := newRun(t)
	r.toVerify()
	before, _ := json.Marshal(r.j.State())
	raw, _ := json.Marshal(r.j.Events)
	var events []Event
	json.Unmarshal(raw, &events)
	reopened, err := Open(testProfile, starter(t), events, r.j.Blobs)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(reopened.State())
	if string(before) != string(after) {
		t.Fatalf("state after reopening differs:\n%s\n%s", before, after)
	}
	events[3].Data = json.RawMessage(strings.Replace(string(events[3].Data), "discover", "verify", 1))
	if _, err := Open(testProfile, starter(t), events, r.j.Blobs); err == nil || !strings.Contains(err.Error(), "changed after it was recorded") {
		t.Fatalf("a tampered log was accepted: %v", err)
	}
}

func TestReplayFindsWhereItWentWrong(t *testing.T) {
	r := newRun(t)
	r.act(Action{Type: "submit", Target: "db-host-1", Step: StepDiscover, Data: map[string]any{"paste": "ls -la"}})
	r.submit(map[string]any{"paste": block("DISCOVER", discoverLines...) + "\nexport DB_PASSWORD=hunter2"})
	r.submit(map[string]any{"components": map[string]any{"postgres": true, "app": true}, "vars": map[string]any{}})
	r.submit(map[string]any{"choice": "proceed"})
	r.submit(map[string]any{"paste": block("APPLY", "host=db-host-1", "placeholder.1=OTEL_ACCESS_TOKEN", "placeholders=1", "result=placeholders")})
	r.act(Action{Type: "note", Data: map[string]any{"text": "I do not have the token yet"}})

	raw, name, err := r.j.ExportBundle(r.now, "Safari")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		// The zip is compressed, so check the loaded form as well.
		t.Error("a pasted secret is in the bundle")
	}
	b, err := LoadBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range b.Blobs {
		if strings.Contains(content, "hunter2") {
			t.Fatal("a pasted secret is in the bundle")
		}
	}
	for _, e := range b.Events {
		if strings.Contains(string(e.Data), "hunter2") {
			t.Fatal("a pasted secret is in the event log")
		}
	}
	code := SessionCode(r.j.Events)
	if !strings.Contains(name, code) {
		t.Errorf("bundle name %q does not carry session code %s", name, code)
	}

	rep := Analyze(b, nil, code)
	if !rep.ChainOK || !rep.CodeMatches || !rep.Rerun.Same {
		t.Fatalf("clean bundle reported as suspect: chain=%v code=%v rerun=%+v", rep.ChainOK, rep.CodeMatches, rep.Rerun)
	}
	if !strings.Contains(rep.Verdict, `db-host-1 stopped at "Install on the server" (Install failed: placeholders: OTEL_ACCESS_TOKEN)`) {
		t.Errorf("verdict does not name the cause: %s", rep.Verdict)
	}
	titles := ""
	for _, f := range rep.Findings {
		titles += f.Severity + ": " + f.Title + "\n"
	}
	for _, want := range []string{"problem: Install failed: placeholders", "warning: Input not accepted (attempt 1", "info: Note from the user"} {
		if !strings.Contains(titles, want) {
			t.Errorf("finding %q missing from:\n%s", want, titles)
		}
	}
	var sawScreens int
	for _, s := range rep.Steps {
		if strings.Contains(s.Saw, "Step ") {
			sawScreens++
		}
	}
	if sawScreens < 5 {
		t.Errorf("only %d steps carry the screen the user saw", sawScreens)
	}

	pdf, err := RenderPDF(rep)
	if err != nil || !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Fatalf("PDF not produced: %v", err)
	}
	if out := os.Getenv("OTELGENIE_PDF_OUT"); out != "" {
		os.WriteFile(out, pdf, 0o644)
		os.WriteFile(strings.TrimSuffix(out, ".pdf")+".zip", raw, 0o644)
	}

	// A wrong session code, an edited log, and a changed template are each caught.
	if rep := Analyze(b, nil, "AAAA-BBBB"); rep.CodeMatches {
		t.Error("a wrong session code was accepted")
	}
	b.Snap.Files["catalog/postgres/logs.yaml"] = strings.Replace(b.Snap.Files["catalog/postgres/logs.yaml"], "start_at: end", "start_at: beginning", 1)
	if rep := Analyze(b, nil, code); rep.Rerun.Same || len(rep.Rerun.Diffs) == 0 {
		t.Error("re-run did not notice that the template changed")
	} else if len(rep.Rerun.Diffs) > 4 {
		t.Errorf("one changed file produced %d differences:\n%s", len(rep.Rerun.Diffs), strings.Join(rep.Rerun.Diffs, "\n"))
	}
	b.Events[5].TS = "2030-01-01T00:00:00Z"
	if rep := Analyze(b, nil, code); rep.ChainOK || !strings.Contains(rep.Verdict, "damaged") {
		t.Error("an edited event was not detected")
	}
}

func TestArchiveIsWhatTheServerNeeds(t *testing.T) {
	r := newRun(t)
	r.toVerify()
	raw, name, err := r.j.TargetArchive("db-host-1")
	if err != nil || name != "otelgenie-db-host-1.tar.gz" {
		t.Fatal(name, err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, name), raw, 0o644)
	if out, err := exec.Command("tar", "xzf", filepath.Join(dir, name), "-C", dir).CombinedOutput(); err != nil {
		t.Fatalf("tar could not read the archive: %v %s", err, out)
	}
	root := filepath.Join(dir, "otelgenie-db-host-1")
	if info, err := os.Stat(filepath.Join(root, "apply.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("apply.sh missing or not executable: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "otelcol.env")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("otelcol.env should be private: %v", err)
	}
	check := exec.Command("shasum", "-a", "256", "-c", "MANIFEST.sha256")
	check.Dir = root
	if out, err := check.CombinedOutput(); err != nil {
		t.Errorf("the manifest does not verify with standard tools: %v\n%s", err, out)
	}
}

// The generated verify.sh is run for real against a status page captured from
// a running collector, and its output goes through the engine.
func TestRealVerifyScriptOutputIsUnderstood(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	r := newRun(t)
	r.toVerify()
	cmd := exec.Command("bash", filepath.Join("..", "template-starter", "scripts", "vm", "verify.sh"))
	cmd.Env = append(os.Environ(), "OTELGENIE_METRICS_FILE="+filepath.Join("testdata", "collector-metrics.txt"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	r.submit(map[string]any{"paste": string(out)})
	tg := r.j.Targets["db-host-1"]
	if tg.LastReject != "" {
		t.Fatalf("real script output was rejected: %s", tg.LastReject)
	}
	rows := map[string]VerifyRow{}
	for _, row := range tg.Verify.Rows {
		rows[row.Name] = row
	}
	if rows["hostmetrics/host"].Status != "receiving" || rows["hostmetrics/host"].Count == 0 {
		t.Errorf("host metrics not recognised from real output: %+v", rows["hostmetrics/host"])
	}
	if rows["otlphttp"].Status != "not-delivering" {
		t.Errorf("the fixture's exporter never delivered, but got: %+v", rows["otlphttp"])
	}
}

func TestOneReceiverCanServeSeveralSignals(t *testing.T) {
	entry := func(signal, port string) string {
		return fmt.Sprintf(`#! otelgenie component=app signal=%[1]s targets=vm title="Application %[1]s"
receivers:
  otlp/app:
    protocols:
      http:
        endpoint: 127.0.0.1:%[2]s
service:
  pipelines:
    %[1]s/app:
      receivers: [otlp/app]
`, signal, port)
	}
	files := map[string]string{
		"catalog/base.yaml":       starter(t).Files["catalog/base.yaml"],
		"catalog/app/traces.yaml": entry("traces", "4318"),
		"catalog/app/logs.yaml":   entry("logs", "4318"),
	}
	if _, problems := LoadCatalog(files); len(problems) > 0 {
		t.Fatalf("a receiver repeated word for word within a component was rejected:\n%s", strings.Join(problems, "\n"))
	}
	files["catalog/app/logs.yaml"] = entry("logs", "9999")
	if _, problems := LoadCatalog(files); !strings.Contains(strings.Join(problems, "\n"), `"otlp/app" is also defined in`) {
		t.Fatalf("two different receivers with one name were not reported: %v", problems)
	}

	// Spans arriving say nothing about metrics or logs on the same receiver,
	// and an application that has not started sending is not a failure.
	r := newRun(t)
	r.toVerify()
	r.submit(map[string]any{"paste": r.verifyBlock(append([]string{"m.otelcol_receiver_accepted_spans|otlp/app=5"}, healthy...)...)})
	if r.step() != StepDone {
		t.Fatalf("verify did not finish: %+v", r.j.Targets["db-host-1"].Verify)
	}
	status := map[string]string{}
	for _, row := range r.j.Targets["db-host-1"].Verify.Rows {
		if row.Name == "otlp/app" {
			status[row.Signal] = row.Status
		}
	}
	if status["traces"] != "receiving" || status["metrics"] != "silent" || status["logs"] != "silent" {
		t.Fatalf("otlp/app should be receiving traces only: %v", status)
	}
}
