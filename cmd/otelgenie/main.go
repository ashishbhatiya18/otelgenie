// Command otelgenie is the page and its template in one program.
//
//	otelgenie serve     opens the page on this machine
//	otelgenie update    fetches the latest template from where it is kept
//	otelgenie config    says where that is
//
// The page is the same static site that can be hosted anywhere; served from
// here it also gets its template from this program, so nobody has to load
// one by hand.
package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"otelgenie/engine"
)

//go:embed all:ui
var embedded embed.FS

// version is set when a release is built.
var version = "dev"

const (
	// The page is always at this address, so a bookmark keeps working and the
	// background service and a hand-started copy cannot both be running unnoticed.
	defaultAddr = "127.0.0.1:11111"
	formula     = "ashishbhatiya18/tap/otelgenie"
	maxFile     = 512 * 1024
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

const usage = `otelgenie: from a collector endpoint to working telemetry

  otelgenie install
      Keep the page running in the background, from now on and after every restart,
      as a Homebrew service. It is then always at http://127.0.0.1:11111/
  otelgenie uninstall
      Stop the background service.

  otelgenie serve [--addr 127.0.0.1:11111] [--open]
      Run the page in this terminal instead, until Ctrl+C. Everything you enter stays
      on this machine, in files this program keeps.

  otelgenie update
      Fetch the latest template from where it is kept, and check it.

  otelgenie config
      Show where the template is kept.
  otelgenie config template-url URL-OR-FOLDER
      A git address, for example https://github.example.com/observability/otel-templates
      or git@github.example.com:observability/otel-templates.git, or a folder on this machine.
  otelgenie config template-ref BRANCH-TAG-OR-COMMIT
      Which version to fetch. Empty means the default branch.

  otelgenie version
`

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "serve":
		err = serve(args[1:], out)
	case "install":
		err = install(out, errOut)
	case "uninstall":
		err = uninstall(out, errOut)
	case "update":
		err = update(out, errOut)
	case "config":
		err = configure(args[1:], out)
	case "version", "--version":
		fmt.Fprintln(out, "otelgenie", version)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
	default:
		fmt.Fprintf(errOut, "otelgenie: there is no command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, "otelgenie:", err)
		return 1
	}
	return 0
}

// --- where things are kept ---------------------------------------------------

// Config is what the user has told the program.
type Config struct {
	TemplateURL string `json:"template_url"`
	TemplateRef string `json:"template_ref"`
}

// Fetched records the last successful update.
type Fetched struct {
	URL     string `json:"url"`
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
	Updated string `json:"updated"`
}

func home() (string, error) {
	if dir := os.Getenv("OTELGENIE_HOME"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "otelgenie"), nil
}

func readJSON(name string, v any) error {
	dir, err := home()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func writeJSON(name string, v any) error {
	dir, err := home()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(filepath.Join(dir, name), append(raw, '\n'), 0o644)
}

func loadConfig() (Config, error) {
	var c Config
	err := readJSON("config.json", &c)
	if v := os.Getenv("OTELGENIE_TEMPLATE_URL"); v != "" {
		c.TemplateURL = v
	}
	if v := os.Getenv("OTELGENIE_TEMPLATE_REF"); v != "" {
		c.TemplateRef = v
	}
	return c, err
}

func configure(args []string, out io.Writer) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		dir, _ := home()
		fmt.Fprintf(out, "template-url  %s\ntemplate-ref  %s\nkept in       %s\n", orNone(c.TemplateURL), orNone(c.TemplateRef), dir)
		return nil
	case len(args) == 2 && args[0] == "template-url":
		c.TemplateURL = strings.TrimSpace(args[1])
		// A folder is remembered by its full path, so serve finds it from anywhere.
		if isDir(c.TemplateURL) {
			if abs, err := filepath.Abs(c.TemplateURL); err == nil {
				c.TemplateURL = abs
			}
		}
	case len(args) == 2 && args[0] == "template-ref":
		c.TemplateRef = strings.TrimSpace(args[1])
	default:
		return fmt.Errorf("use: otelgenie config template-url URL-OR-FOLDER, or otelgenie config template-ref REF")
	}
	if err := writeJSON("config.json", c); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved. Run \"otelgenie update\" to fetch the template.\n")
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// --- the template ------------------------------------------------------------

// readTemplate reads the files a template is made of from a folder.
func readTemplate(dir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel != "otelgenie.json" && !strings.HasPrefix(rel, "catalog/") && !strings.HasPrefix(rel, "scripts/") {
			return nil
		}
		if st, err := d.Info(); err != nil || st.Size() > maxFile {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(raw)
		return nil
	})
	return files, err
}

// Template is what the page is given.
type Template struct {
	Snapshot *engine.Snapshot `json:"snapshot"`
	Source   Fetched          `json:"source"`
	// Live is set when the template is a folder on this machine, read anew
	// every time rather than fetched.
	Live     bool     `json:"live"`
	Problems []string `json:"problems"`
}

// errNoTemplate tells the user what to do, rather than what went wrong.
type errNoTemplate struct{ hint string }

func (e errNoTemplate) Error() string { return e.hint }

// snapshotOf builds and checks a snapshot. commit is empty for a folder that
// is not a fetched copy; its content then stands in for one.
func snapshotOf(dir, repo, commit string) (*engine.Snapshot, []string, error) {
	files, err := readTemplate(dir)
	if err != nil {
		return nil, nil, err
	}
	var snap *engine.Snapshot
	if commit == "" {
		if snap, err = engine.SnapshotFromFolder(files); err != nil {
			return nil, nil, err
		}
		snap.Repo = repo
	} else {
		snap = engine.SnapshotFromFiles(repo, commit, files)
		if _, err := snap.Manifest(); err != nil {
			return nil, nil, err
		}
	}
	_, problems := engine.LoadCatalog(snap.Files)
	return snap, problems, nil
}

// current is the template the page should use right now.
func current() (*Template, error) {
	c, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if c.TemplateURL == "" {
		return nil, errNoTemplate{"No template is set. Run: otelgenie config template-url URL-OR-FOLDER, then otelgenie update"}
	}
	if isDir(c.TemplateURL) {
		snap, problems, err := snapshotOf(c.TemplateURL, c.TemplateURL, "")
		if err != nil {
			return nil, err
		}
		return &Template{Snapshot: snap, Source: Fetched{URL: c.TemplateURL, Commit: snap.Commit}, Live: true, Problems: problems}, nil
	}
	var got Fetched
	if err := readJSON("fetched.json", &got); err != nil {
		return nil, err
	}
	dir, err := home()
	if err != nil {
		return nil, err
	}
	if got.Commit == "" || !isDir(filepath.Join(dir, "template")) {
		return nil, errNoTemplate{"The template has not been fetched yet. Run: otelgenie update"}
	}
	snap, problems, err := snapshotOf(filepath.Join(dir, "template"), got.URL, got.Commit)
	if err != nil {
		return nil, err
	}
	return &Template{Snapshot: snap, Source: got, Problems: problems}, nil
}

// git runs git without ever stopping to ask a question: extra holds settings
// for this one run, such as a sign-in, that are not written anywhere.
func git(out, errOut io.Writer, extra []string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	cmd.Env = append(cmd.Env, extra...)
	return cmd.Run()
}

// way is one way of reaching the template: an address, and how to sign in.
type way struct {
	name  string
	url   string
	extra []string
}

// ways lists how to try an address, most ordinary first. An https address is
// often typed where the machine actually signs in another way, so after the
// address as given come a token (from the environment, or from the gh
// program if it is signed in to that host) and the same repository over SSH.
func ways(address string) []way {
	list := []way{{name: "", url: address}}
	rest, ok := strings.CutPrefix(address, "https://")
	if !ok {
		return list
	}
	host, repo, ok := strings.Cut(rest, "/")
	if !ok || strings.Contains(host, "@") {
		return list
	}
	token, from := "", ""
	for _, name := range []string{"OTELGENIE_TEMPLATE_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			token, from = v, "the token in "+name
			break
		}
	}
	if token == "" {
		if raw, err := exec.Command("gh", "auth", "token", "--hostname", host).Output(); err == nil {
			token, from = strings.TrimSpace(string(raw)), "the sign-in of the gh program"
		}
	}
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		// Handed over in the environment, so it is in no command line and no file.
		list = append(list, way{name: from, url: address, extra: []string{
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
		}})
	}
	list = append(list, way{name: "SSH", url: "git@" + host + ":" + strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git") + ".git"})
	return list
}

// clone fetches one version of the template into dir.
func clone(w way, ref, dir string) error {
	os.RemoveAll(dir)
	args := []string{"clone", "--quiet", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	if git(io.Discard, io.Discard, w.extra, append(args, w.url, dir)...) == nil {
		return nil
	}
	// The version may be a commit, which cannot be asked for by name.
	os.RemoveAll(dir)
	if err := git(io.Discard, io.Discard, w.extra, "clone", "--quiet", w.url, dir); err != nil {
		return err
	}
	if ref != "" {
		if err := git(io.Discard, io.Discard, nil, "-C", dir, "checkout", "--quiet", ref); err != nil {
			return errNoVersion
		}
	}
	return nil
}

var errNoVersion = errors.New("no such version")

func update(out, errOut io.Writer) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if c.TemplateURL == "" {
		return fmt.Errorf("no template is set. Run first: otelgenie config template-url URL-OR-FOLDER")
	}
	if isDir(c.TemplateURL) {
		t, err := current()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "The template is the folder %s. It is read anew every time the page loads, so there is nothing to fetch.\n", c.TemplateURL)
		return report(out, t)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is needed to fetch the template and is not installed")
	}
	dir, err := home()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fresh, kept := filepath.Join(dir, "template.new"), filepath.Join(dir, "template")
	os.RemoveAll(fresh)
	defer os.RemoveAll(fresh)

	fmt.Fprintf(out, "Fetching %s", c.TemplateURL)
	if c.TemplateRef != "" {
		fmt.Fprintf(out, " at %s", c.TemplateRef)
	}
	fmt.Fprintln(out)
	var fetched error = errors.New("not tried")
	for _, w := range ways(c.TemplateURL) {
		if fetched = clone(w, c.TemplateRef, fresh); fetched == nil {
			if w.name != "" {
				fmt.Fprintf(out, "Signed in with %s.\n", w.name)
			}
			break
		}
		if errors.Is(fetched, errNoVersion) {
			return fmt.Errorf("the template has no version %q", c.TemplateRef)
		}
	}
	if fetched != nil {
		return fmt.Errorf("the template could not be fetched from %s. Either the address is wrong, the network does not reach it, or git on this machine is not signed in to it. "+
			"To see git's own answer, run: git ls-remote %s\nIf it asks for a name and password, give otelgenie a way to sign in: use the SSH address (git@HOST:OWNER/REPO.git), "+
			"or sign in with the gh program (gh auth login), or set OTELGENIE_TEMPLATE_TOKEN to an access token", c.TemplateURL, c.TemplateURL)
	}
	var head strings.Builder
	if err := git(&head, errOut, nil, "-C", fresh, "rev-parse", "HEAD"); err != nil {
		return fmt.Errorf("the fetched template could not be read")
	}
	got := Fetched{URL: c.TemplateURL, Ref: c.TemplateRef, Commit: strings.TrimSpace(head.String()), Updated: time.Now().UTC().Format(time.RFC3339)}
	snap, problems, err := snapshotOf(fresh, got.URL, got.Commit)
	if err != nil {
		return fmt.Errorf("what was fetched is not a usable template (%v). The template in use was left as it was", err)
	}
	t := &Template{Snapshot: snap, Source: got, Problems: problems}
	if len(problems) > 0 {
		report(out, t)
		return fmt.Errorf("the fetched template has problems. The template in use was left as it was")
	}
	var before Fetched
	readJSON("fetched.json", &before)
	if err := os.RemoveAll(kept); err != nil {
		return err
	}
	if err := os.Rename(fresh, kept); err != nil {
		return err
	}
	if err := writeJSON("fetched.json", got); err != nil {
		return err
	}
	switch {
	case before.Commit == got.Commit:
		fmt.Fprintln(out, "Already up to date.")
	case before.Commit == "":
		fmt.Fprintln(out, "Fetched.")
	default:
		fmt.Fprintf(out, "Updated from %s.\n", short(before.Commit))
	}
	return report(out, t)
}

func short(commit string) string {
	if len(commit) > 10 {
		return commit[:10]
	}
	return commit
}

func report(out io.Writer, t *Template) error {
	man, err := t.Snapshot.Manifest()
	if err != nil {
		return err
	}
	cat, _ := engine.LoadCatalog(t.Snapshot.Files)
	var names []string
	for _, c := range cat.Components("") {
		if !c.Always {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	fmt.Fprintf(out, "Template %s at %s: collector %s, %d files, %d components (%s)\n",
		man.Name, short(t.Snapshot.Commit), man.CollectorVersion, len(t.Snapshot.Files), len(names), strings.Join(names, ", "))
	for _, p := range t.Problems {
		fmt.Fprintln(out, "  problem:", p)
	}
	if len(t.Problems) > 0 {
		fmt.Fprintln(out, "Send these to the template's owner.")
	}
	return nil
}

// --- the page ----------------------------------------------------------------

// headers reads the page's own _headers file, so that the rules a hosted
// copy is served under (above all: nothing leaves the browser) hold here too.
func headers(ui fs.FS) http.Header {
	h := http.Header{}
	raw, err := fs.ReadFile(ui, "_headers")
	if err != nil {
		return h
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			continue
		}
		if name, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			h.Set(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}
	return h
}

// maxBody is the most the page may send in one request: a packaged Helm
// chart or a session file, as text.
const maxBody = 64 << 20

// api runs one of the page's requests and writes the answer, or the reason
// there is none, as JSON.
func api(w http.ResponseWriter, r *http.Request, do func(name string, payload []byte) (any, error)) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if r.Method != http.MethodPost {
		fail(http.StatusMethodNotAllowed, "this address takes POST requests only")
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		fail(http.StatusRequestEntityTooLarge, "that is too large to read")
		return
	}
	out, err := func() (out any, err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("internal error: %v", p)
			}
		}()
		return do(r.PathValue("name"), payload)
	}()
	if err != nil {
		fail(http.StatusUnprocessableEntity, err.Error())
		return
	}
	if out == nil {
		out = map[string]any{}
	}
	json.NewEncoder(w).Encode(out)
}

// guard keeps other web sites away from the program. A page open in the same
// browser could otherwise send requests to this address, or reach it under a
// name of its own choosing. allowed is an extra origin for work on the page
// itself, where it is served by a development server.
func guard(next http.Handler, allowed string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		cross := origin != "" && origin != "http://"+r.Host
		if cross && (allowed == "" || origin != allowed) {
			http.Error(w, "this address is only for the otelgenie page", http.StatusForbidden)
			return
		}
		if cross {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		// A request that does not say it is JSON did not come from the page.
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handler(ui fs.FS) http.Handler {
	extra := headers(ui)
	files := http.FileServer(http.FS(ui))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/call/{name}", func(w http.ResponseWriter, r *http.Request) {
		api(w, r, func(name string, payload []byte) (any, error) {
			engineLock.Lock()
			defer engineLock.Unlock()
			return dispatch(name, payload)
		})
	})
	mux.HandleFunc("/api/store/{name}", func(w http.ResponseWriter, r *http.Request) { api(w, r, store) })
	mux.HandleFunc("/api/template", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		t, err := current()
		var none errNoTemplate
		switch {
		case errors.As(err, &none):
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"hint": none.hint})
		case err != nil:
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"hint": "The template could not be read: " + err.Error()})
		default:
			json.NewEncoder(w).Encode(t)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for name, values := range extra {
			w.Header()[name] = values
		}
		// The page changes when this program is upgraded; never serve an old one.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	return mux
}

func serve(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := flags.String("addr", defaultAddr, "address to listen on")
	open := flags.Bool("open", false, "open the page in the browser")
	dir := flags.String("ui", "", "serve the page from this folder instead of the one built in")
	dev := flags.String("allow-origin", "", "for work on the page itself: also answer a development server at this origin, for example http://localhost:3000")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var ui fs.FS
	if *dir != "" {
		if !isDir(*dir) {
			return fmt.Errorf("%s is not a folder", *dir)
		}
		ui = os.DirFS(*dir)
	} else {
		ui, _ = fs.Sub(embedded, "ui")
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s (%v). If otelgenie is already running, as the background service or in another terminal, the page is at http://%s/ and there is nothing to start", *addr, err, *addr)
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Fprintf(out, "otelgenie %s is at %s\n", version, url)
	if t, err := current(); err != nil {
		fmt.Fprintln(out, err)
	} else {
		report(out, t)
	}
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); host != "127.0.0.1" && host != "::1" && host != "localhost" {
		fmt.Fprintln(out, "It is listening beyond this machine. Anyone who can reach that address can open it.")
	}
	fmt.Fprintln(out, "Press Ctrl+C to stop.")
	if *open {
		openBrowser(url)
	}
	return http.Serve(ln, guard(handler(ui), *dev))
}

// --- the background service --------------------------------------------------

// The service is Homebrew's: the formula says how to run the program, and
// "brew services" registers it with the system (launchd on a Mac, systemd on
// Linux) so that it starts at login and is restarted if it stops.

func brew(out, errOut io.Writer, args ...string) error {
	cmd := exec.Command("brew", args...)
	cmd.Stdout, cmd.Stderr = out, errOut
	return cmd.Run()
}

func answering() bool {
	conn, err := net.DialTimeout("tcp", defaultAddr, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func install(out, errOut io.Writer) error {
	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("the background service is run by Homebrew, which is not installed here. Install Homebrew, or keep \"otelgenie serve\" running yourself")
	}
	if brew(io.Discard, io.Discard, "list", "--formula", formula) != nil {
		return fmt.Errorf("this copy of otelgenie was not installed with Homebrew, so Homebrew cannot run it as a service. Install it with: brew install %s", formula)
	}
	// restart rather than start: after an upgrade the service must run the new program.
	if err := brew(out, errOut, "services", "restart", formula); err != nil {
		return fmt.Errorf("Homebrew could not start the service. If something else is using %s, stop it first; see: brew services info otelgenie", defaultAddr)
	}
	for i := 0; i < 20 && !answering(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !answering() {
		return fmt.Errorf("the service was registered but nothing is answering at %s yet. Look at its log: %s", defaultAddr, "$(brew --prefix)/var/log/otelgenie.log")
	}
	fmt.Fprintf(out, "otelgenie is running in the background, at http://%s/\nIt starts again by itself after a restart. To stop it: otelgenie uninstall\n", defaultAddr)
	if _, err := current(); err != nil {
		fmt.Fprintln(out, err)
	}
	return nil
}

func uninstall(out, errOut io.Writer) error {
	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("Homebrew is not installed here, so there is no background service to stop")
	}
	if err := brew(out, errOut, "services", "stop", formula); err != nil {
		return fmt.Errorf("Homebrew could not stop the service; see: brew services info otelgenie")
	}
	fmt.Fprintln(out, "The background service is stopped. Your settings and projects are kept.")
	return nil
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}
