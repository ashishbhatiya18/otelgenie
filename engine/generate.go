package engine

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	etcDir      = "/etc/otelcol-otelgenie"
	placeholder = "CHANGE_ME"
)

// endpointHostPort reads the collector endpoint the user was given. The
// starter template exports over OTLP/HTTP, so a URL is expected.
func endpointHostPort(endpoint string) (string, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("the collector endpoint should look like https://collector.example.com:4318")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Hostname(), port, nil
}

// sendsTo is where a target of this kind sends: servers go through the
// cluster's Ingress when the project has one, everything else straight to
// the destination.
func (j *Journey) sendsTo(kind string) string {
	if kind == kindVM && j.In.Ingress != "" {
		return j.In.Ingress
	}
	return j.In.Endpoint
}

// selectedEntries is what a target will actually run.
func (j *Journey) selectedEntries(t *Target) []*Entry {
	return j.Cat.Select(t.Kind, t.Selected, j.In.Signals)
}

// candidateEntries is everything the user asked for, before discovery has
// said what is really on this server.
func (j *Journey) candidateEntries(kind string) []*Entry {
	return j.Cat.Select(kind, j.In.Components, j.In.Signals)
}

func (j *Journey) discoverItems(kind string) string {
	listed := map[string]bool{"port": true, "unit": true, "glob": true}
	if kind == kindK8s {
		listed = map[string]bool{"svc": true}
	}
	seen := map[string]bool{}
	var lines []string
	for _, e := range j.candidateEntries(kind) {
		for _, p := range e.Probes {
			if !listed[p.Kind] {
				continue
			}
			line := p.Kind + " " + p.Arg
			if !seen[line] {
				seen[line] = true
				lines = append(lines, line)
			}
		}
	}
	sort.Strings(lines)
	// A cluster cannot be asked to open a connection without running
	// something in it, which discovery never does.
	if host, port, err := endpointHostPort(j.sendsTo(kind)); err == nil && kind != kindK8s {
		lines = append(lines, "tcp "+host+" "+port)
	}
	return strings.Join(lines, "\n") + "\n"
}

func (j *Journey) discoverFiles(kind string) map[string]string {
	return map[string]string{
		"discover.sh":    j.Snap.Files["scripts/"+kind+"/discover.sh"],
		"discover.items": j.discoverItems(kind),
	}
}

// svcAddress turns what discovery reported for a Service, "namespace:port",
// into the address a probe asks for.
func svcAddress(p Probe, fact string) string {
	ns, port, ok := strings.Cut(fact, ":")
	if !ok || ns == "" {
		return ""
	}
	return strings.NewReplacer("{name}", p.Arg, "{ns}", ns, "{port}", port).Replace(p.As)
}

// evidence says in plain words why a component is believed to be on a server.
func (j *Journey) evidence(t *Target, component string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range j.candidateEntries(t.Kind) {
		if e.Component != component {
			continue
		}
		for _, p := range e.Probes {
			var msg string
			v := t.Facts[p.Kind+"."+p.Arg]
			switch {
			case p.Kind == "distro" && t.Facts["distro"] == p.Arg:
				msg = "the cluster reports itself as " + p.Arg
			case p.Kind == "port" && v == "open":
				msg = "port " + p.Arg + " is open"
			case p.Kind == "unit" && v == "active":
				msg = "service " + p.Arg + " is running"
			case p.Kind == "glob" && v != "" && v != "0" && v != "denied":
				msg = v + " file(s) match " + p.Arg
			}
			if msg != "" && !seen[msg] {
				seen[msg] = true
				out = append(out, msg)
			}
		}
	}
	return out
}

func (j *Journey) memoryMaxMB(t *Target) int {
	max := j.Man.Settings.MemoryMaxMB
	if kb, err := strconv.Atoi(t.Facts["mem_total_kb"]); err == nil && kb > 0 {
		// Never take more than a tenth of the server, and never less than
		// the collector needs to run at all.
		tenth := kb / 1024 / 10
		if tenth < 128 {
			tenth = 128
		}
		if tenth < max {
			max = tenth
		}
	}
	return max
}

// resolveVars fills each collector env variable from, in order: the journey
// inputs, what the user typed, and what discovery found. Variables left out
// fall back to the default written in the catalog file.
func (j *Journey) resolveVars(t *Target, components []string, form map[string]string) (vars map[string]string, typed, missing []string) {
	vars = map[string]string{}
	for _, e := range j.Cat.Select(t.Kind, components, j.In.Signals) {
		for _, p := range e.Probes {
			if p.Var == "" || p.Kind == "pod" {
				continue
			}
			if v := strings.TrimSpace(form[p.Var]); v != "" && p.Kind != "input" {
				if _, done := vars[p.Var]; !done {
					typed = append(typed, p.Var)
				}
				vars[p.Var] = v
				continue
			}
			if _, done := vars[p.Var]; done {
				continue
			}
			switch p.Kind {
			case "input":
				switch p.Arg {
				case "endpoint":
					vars[p.Var] = j.sendsTo(t.Kind)
				case "project":
					vars[p.Var] = j.In.Project
				case "env":
					vars[p.Var] = j.In.Env
				case "memory_limit_mib":
					vars[p.Var] = strconv.Itoa(j.memoryMaxMB(t) * 8 / 10)
				case "target":
					vars[p.Var] = t.Name
				case "header_dimensions":
					vars[p.Var] = j.headerDimensions()
				case "request_headers":
					vars[p.Var] = strings.Join(j.capturedHeaders("request"), ",")
				case "response_headers":
					vars[p.Var] = strings.Join(j.capturedHeaders("response"), ",")
				case "ca_file":
					if strings.TrimSpace(j.Profile.CABundle) != "" {
						vars[p.Var] = etcDir + "/ca-bundle.pem"
						if t.Kind == kindK8s {
							vars[p.Var] = k8sCADir + "/ca-bundle.pem"
						}
					}
				}
			case "ask":
				missing = append(missing, p.Var)
			case "port":
				if t.Facts["port."+p.Arg] == "open" {
					vars[p.Var] = strings.ReplaceAll(p.As, "{port}", p.Arg)
				}
			case "glob":
				if v := t.Facts["glob."+p.Arg]; v != "" && v != "0" && v != "denied" {
					vars[p.Var] = p.Arg
				}
			case "svc":
				if v := svcAddress(p, t.Facts["svc."+p.Arg]); v != "" {
					vars[p.Var] = v
				}
			}
		}
	}
	sort.Strings(typed)
	sort.Strings(missing)
	return vars, typed, missing
}

// OpenNeed is something a person outside this journey has to provide.
type OpenNeed struct {
	Component string
	Kind      string
	Name      string
	Owner     string
	Note      string
}

// openNeeds lists credentials to be sourced and exporters that discovery did
// not find. The access token is left out: the user already holds it.
func (j *Journey) openNeeds(t *Target) []OpenNeed {
	var out []OpenNeed
	for _, e := range j.selectedEntries(t) {
		if e.Signal == "base" {
			continue
		}
		for _, n := range e.Needs {
			switch n.Kind {
			case "credential":
				out = append(out, OpenNeed{e.Component, n.Kind, n.Name, n.Attrs["owner"], n.Attrs["note"]})
			case "exporter":
				port := n.Attrs["port"]
				if t.Facts["port."+port] == "open" {
					continue
				}
				typedAddr := false
				for _, p := range e.Probes {
					if p.Kind == "port" && p.Arg == port && contains(t.Typed, p.Var) {
						typedAddr = true
					}
				}
				if !typedAddr {
					out = append(out, OpenNeed{e.Component, n.Kind, n.Name, n.Attrs["owner"], n.Attrs["note"]})
				}
			}
		}
	}
	return out
}

func (j *Journey) secretVars(t *Target) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range j.selectedEntries(t) {
		for _, n := range e.Needs {
			if n.Kind == "credential" && !seen[n.Name] {
				seen[n.Name] = true
				out = append(out, n.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func confName(e *Entry) string {
	if e.Signal == "base" {
		return "conf.d/00-base.yaml"
	}
	return "conf.d/10-" + e.Component + "-" + e.Signal + ".yaml"
}

// generate builds every file that goes to the server. Catalog files are
// copied byte for byte; all the variation lives in the env file.
func (j *Journey) generate(t *Target) map[string]string {
	if t.Kind == kindK8s {
		return j.generateK8s(t)
	}
	files := map[string]string{}
	entries := j.selectedEntries(t)
	for _, e := range entries {
		files[confName(e)] = e.Body
	}
	for _, name := range []string{"discover.sh", "apply.sh", "verify.sh", "rollback.sh"} {
		if body, ok := j.Snap.Files["scripts/vm/"+name]; ok {
			files[name] = body
		}
	}
	files["discover.items"] = j.discoverItems(t.Kind)

	files["otelcol.env"] = j.envFile(t)

	mem := j.memoryMaxMB(t)
	files["settings.env"] = fmt.Sprintf("SERVICE_USER=%s\nMEMORY_MAX_MB=%d\nGOMEMLIMIT_MIB=%d\nCPU_QUOTA_PERCENT=%d\n",
		j.Man.Settings.ServiceUser, mem, mem*8/10, j.Man.Settings.CPUQuotaPercent)

	if pem := strings.TrimSpace(j.Profile.CABundle); pem != "" {
		files["ca-bundle.pem"] = pem + "\n"
	}
	j.addRequests(t, files)
	files["RUNBOOK.md"] = j.runbook(t, entries)

	sealFiles(files)
	return files
}

// envFile holds the value of every variable the configuration reads.
func (j *Journey) envFile(t *Target) string {
	var env strings.Builder
	env.WriteString("# Values for the collector configuration in conf.d. Generated by otelgenie.\n")
	if t.Kind == kindK8s {
		env.Reset()
		env.WriteString("# Values for the collector configuration. apply.sh stores them in the Secret otelgenie-env.\n")
	}
	env.WriteString("# Lines set to " + placeholder + " are secrets: apply.sh asks for them on the " + place(t) + ".\n")
	for _, name := range sortedKeys(t.Vars) {
		fmt.Fprintf(&env, "%s=%s\n", name, t.Vars[name])
	}
	for _, name := range j.secretVars(t) {
		fmt.Fprintf(&env, "%s=%s\n", name, placeholder)
	}
	return env.String()
}

// addRequests writes a ready-to-send note for everything another person has
// to provide.
func (j *Journey) addRequests(t *Target, files map[string]string) {
	for _, n := range j.openNeeds(t) {
		who := n.Owner
		if who == "" {
			who = "the owner of " + n.Component
		}
		body := fmt.Sprintf("# Request for %s\n\nTo: %s\nRegarding: %s on %s %s (%s / %s)\n\n%s\n",
			n.Name, who, n.Component, place(t), t.Name, j.In.Project, j.In.Env, n.Note)
		switch {
		case n.Kind == "credential" && t.Kind == kindK8s:
			body += fmt.Sprintf("\nWhen you have it, it is typed only where the installer runs: run `bash apply.sh` and type it when asked for %s.\n", n.Name)
		case n.Kind == "credential":
			body += fmt.Sprintf("\nWhen you have it, it is entered on the server only: run `sudo bash apply.sh` and type it when asked for %s.\n", n.Name)
		}
		files["requests/"+n.Component+"-"+n.Name+".md"] = body
	}
}

// sealFiles adds the list of checksums the scripts check before running.
func sealFiles(files map[string]string) {
	var manifest strings.Builder
	for _, path := range sortedKeys(files) {
		fmt.Fprintf(&manifest, "%s  %s\n", sha256Hex(files[path]), path)
	}
	files["MANIFEST.sha256"] = manifest.String()
}

func (j *Journey) archiveName(t *Target) string {
	return "otelgenie-" + t.Name
}

func (j *Journey) runbook(t *Target, entries []*Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Telemetry setup for %s\n\n", t.Name)
	fmt.Fprintf(&b, "Project: %s\nEnvironment: %s\nCollector endpoint: %s\nTemplate: %s at %s\nSetup fingerprint: %s\n\n",
		j.In.Project, j.In.Env, j.sendsTo(kindVM), j.Snap.Repo, j.Snap.Commit, ComputeFingerprint(j.Profile, j.Snap, j.Man).Code)
	b.WriteString("## What this sets up\n\n")
	for _, e := range entries {
		if e.Signal != "base" {
			fmt.Fprintf(&b, "- %s (%s)\n", e.Title, e.Signal)
		}
	}
	fmt.Fprintf(&b, "\nThe collector runs as the systemd service otelgenie-otelcol, limited to %d MB of memory and %d%% of one CPU.\n\n",
		j.memoryMaxMB(t), j.Man.Settings.CPUQuotaPercent)
	b.WriteString("## Steps\n\n")
	fmt.Fprintf(&b, "1. Copy the folder %s to the server.\n", j.archiveName(t))
	b.WriteString("2. On the server, run: sudo bash apply.sh\n")
	b.WriteString("   It asks for each secret, checks the configuration, and starts the service.\n")
	b.WriteString("3. On the server, run: sudo bash verify.sh\n")
	b.WriteString("   It reports whether data is flowing.\n")
	b.WriteString("4. To remove everything apply.sh installed, run: sudo bash rollback.sh\n\n")
	b.WriteString("## Where things are\n\n")
	b.WriteString("- Configuration: " + etcDir + "/conf.d/ (one file per component; upstream documentation applies as written)\n")
	b.WriteString("- Values and secrets: " + etcDir + "/otelcol.env (readable by root only)\n")
	b.WriteString("- Logs: journalctl -u otelgenie-otelcol\n")
	return b.String()
}

// packageHint tells the user how to get the collector onto this server,
// according to the one-time setup.
func (j *Journey) packageHint(t *Target) []string {
	family := "rhel"
	id := t.Facts["os_id"] + " " + t.Facts["os_like"]
	if strings.Contains(id, "debian") || strings.Contains(id, "ubuntu") {
		family = "debian"
	}
	var art *Artifact
	for i, a := range j.Man.Artifacts {
		if a.Kind == "package" && a.OS == family && a.Arch == t.Facts["arch"] {
			art = &j.Man.Artifacts[i]
		}
	}
	install := map[string]string{"rhel": "sudo dnf install -y", "debian": "sudo apt-get install -y"}[family]
	local := map[string]string{"rhel": "sudo rpm -i", "debian": "sudo dpkg -i"}[family]
	if j.Profile.PackageMethod == "repo" {
		return []string{
			"The collector is installed from your internal package repository (" + j.Profile.PackageRepoURL + ").",
			"On the server, run: " + install + " otelcol-contrib",
			"It must be version " + j.Man.CollectorVersion + ".",
		}
	}
	if art == nil {
		return []string{"The template lists no collector package for this server (" + family + ", " + t.Facts["arch"] + "). Ask the template owner to add one."}
	}
	return []string{
		"The collector package has to be carried in by hand, as agreed in the one-time setup.",
		"The file you need is " + art.File + ". On a machine with internet access it is at " + art.URL,
		"Copy it to the server, then run: " + local + " " + art.File,
	}
}
