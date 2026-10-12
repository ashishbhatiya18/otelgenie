package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const starter = "../../template-starter"

func cli(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, &out, &out)
	return out.String(), code
}

func template(t *testing.T) (int, map[string]json.RawMessage) {
	t.Helper()
	ui, _ := os.MkdirTemp("", "ui")
	t.Cleanup(func() { os.RemoveAll(ui) })
	os.WriteFile(filepath.Join(ui, "_headers"), []byte("/*\n  Content-Security-Policy: default-src 'self'\n"), 0o644)
	os.WriteFile(filepath.Join(ui, "index.html"), []byte("<p>page</p>"), 0o644)
	srv := httptest.NewServer(guard(handler(os.DirFS(ui)), ""))
	t.Cleanup(srv.Close)

	page, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(page.Body); string(body) != "<p>page</p>" || page.Header.Get("Content-Security-Policy") != "default-src 'self'" {
		t.Errorf("the page is not served under its own rules: %q, %q", body, page.Header.Get("Content-Security-Policy"))
	}
	res, err := http.Get(srv.URL + "/api/template")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func TestAFolderIsServedAsItIsNow(t *testing.T) {
	t.Setenv("OTELGENIE_HOME", t.TempDir())
	if code, body := template(t); code != http.StatusNotFound || !strings.Contains(string(body["hint"]), "otelgenie config template-url") {
		t.Errorf("with no template set: %d %s", code, body["hint"])
	}
	if out, code := cli(t, "config", "template-url", starter); code != 0 {
		t.Fatal(out)
	}
	out, code := cli(t, "update")
	if code != 0 || !strings.Contains(out, "nothing to fetch") || !strings.Contains(out, "keycloak") {
		t.Errorf("update of a folder: %d\n%s", code, out)
	}
	status, body := template(t)
	var snap struct {
		Commit string
		Files  map[string]string
	}
	json.Unmarshal(body["snapshot"], &snap)
	if status != http.StatusOK || string(body["live"]) != "true" || snap.Files["otelgenie.json"] == "" || snap.Files["scripts/project/otelgenie.sh"] == "" || string(body["problems"]) != "null" {
		t.Errorf("the folder was not served whole: %d, %d files, problems %s", status, len(snap.Files), body["problems"])
	}
}

func TestUpdateFetchesWithGitAndKeepsAGoodTemplate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("OTELGENIE_HOME", t.TempDir())
	repo := t.TempDir()
	sh := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("cp", "-R", starter+"/.", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	sh("init", "--quiet", "--initial-branch", "main")
	sh("add", ".")
	sh("commit", "--quiet", "-m", "first")

	cli(t, "config", "template-url", "file://"+repo)
	if out, code := cli(t, "update"); code != 0 || !strings.Contains(out, "Fetched.") {
		t.Fatalf("first update: %d\n%s", code, out)
	}
	status, body := template(t)
	if status != http.StatusOK || string(body["live"]) != "false" {
		t.Fatalf("the fetched template is not served: %d", status)
	}
	if out, _ := cli(t, "update"); !strings.Contains(out, "Already up to date.") {
		t.Errorf("second update:\n%s", out)
	}

	// A broken template is reported and not put in place of the good one.
	os.WriteFile(filepath.Join(repo, "catalog", "nginx", "metrics.yaml"), []byte("#! otelgenie component=nginx signal=nonsense targets=vm title=x\nreceivers: {}\n"), 0o644)
	sh("commit", "--quiet", "-am", "broken")
	out, code := cli(t, "update")
	if code == 0 || !strings.Contains(out, "problem:") || !strings.Contains(out, "left as it was") {
		t.Errorf("a broken template was accepted: %d\n%s", code, out)
	}
	if _, after := template(t); string(after["snapshot"]) != string(body["snapshot"]) {
		t.Error("the template in use changed although the update failed")
	}
}

func post(t *testing.T, srv *httptest.Server, path, body string, header ...string) (int, map[string]json.RawMessage) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestThePageWorksThroughTheProgram(t *testing.T) {
	t.Setenv("OTELGENIE_HOME", t.TempDir())
	cli(t, "config", "template-url", starter)
	ui := t.TempDir()
	os.WriteFile(filepath.Join(ui, "index.html"), []byte("page"), 0o644)
	srv := httptest.NewServer(guard(handler(os.DirFS(ui)), "http://localhost:3000"))
	defer srv.Close()

	res, _ := http.Get(srv.URL + "/api/template")
	var tpl struct{ Snapshot json.RawMessage }
	json.NewDecoder(res.Body).Decode(&tpl)

	// The engine answers over the same address the page is served from.
	code, out := post(t, srv, "/api/call/describeTemplate", `{"snapshot":`+string(tpl.Snapshot)+`}`)
	if code != http.StatusOK || !strings.Contains(string(out["components"]), `"keycloak"`) {
		t.Fatalf("describeTemplate: %d %s", code, out["error"])
	}
	if code, out = post(t, srv, "/api/call/nonsense", `{}`); code != http.StatusUnprocessableEntity || !strings.Contains(string(out["error"]), "unknown method") {
		t.Errorf("an unknown method: %d %s", code, out["error"])
	}

	// What the page stores comes back, and a project keeps its events in order.
	post(t, srv, "/api/store/set", `{"key":"profile","value":{"registry":"quay.internal"}}`)
	if _, out = post(t, srv, "/api/store/get", `{"key":"profile"}`); !strings.Contains(string(out["value"]), "quay.internal") {
		t.Errorf("a stored value did not come back: %s", out["value"])
	}
	if _, out = post(t, srv, "/api/store/get", `{"key":"never-set"}`); string(out["value"]) != "null" {
		t.Errorf("a value never stored: %s", out["value"])
	}
	post(t, srv, "/api/store/appendDelta", `{"meta":{"id":"j1","project":"P","updated":"2026-01-02"},"delta":{"events":[{"seq":2},{"seq":1}],"blobs":{"aa":"one"}}}`)
	post(t, srv, "/api/store/appendDelta", `{"meta":{"id":"j1","project":"P","updated":"2026-01-03"},"delta":{"events":[{"seq":3},{"seq":2,"again":true}],"blobs":null}}`)
	_, out = post(t, srv, "/api/store/loadJourney", `{"id":"j1"}`)
	if string(out["events"]) != `[{"seq":1},{"seq":2,"again":true},{"seq":3}]` || string(out["blobs"]) != `{"aa":"one"}` {
		t.Errorf("the project was not stored whole: %s %s", out["events"], out["blobs"])
	}
	if _, out = post(t, srv, "/api/store/listJourneys", `{}`); !strings.Contains(string(out["journeys"]), `"2026-01-03"`) {
		t.Errorf("the project is not listed: %s", out["journeys"])
	}
	if code, _ = post(t, srv, "/api/store/loadJourney", `{"id":"../../etc/passwd"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a name that leaves the folder was accepted: %d", code)
	}
	post(t, srv, "/api/store/deleteJourney", `{"id":"j1"}`)
	if code, _ = post(t, srv, "/api/store/loadJourney", `{"id":"j1"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a deleted project is still there: %d", code)
	}

	// Another web site open in the same browser gets nothing.
	if code, _ = post(t, srv, "/api/store/get", `{"key":"profile"}`, "Origin", "https://elsewhere.example"); code != http.StatusForbidden {
		t.Errorf("a request from another site: %d", code)
	}
	if code, _ = post(t, srv, "/api/store/get", `{"key":"profile"}`, "Content-Type", "text/plain"); code != http.StatusUnsupportedMediaType {
		t.Errorf("a request that is not JSON: %d", code)
	}
	if code, _ = post(t, srv, "/api/store/get", `{"key":"profile"}`, "Origin", "http://localhost:3000"); code != http.StatusOK {
		t.Errorf("the development server named on the command line: %d", code)
	}
}
