package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A project can be handed over as one package before any server or cluster
// has been looked at: a README with the whole process, and the files for
// each kind of target built from the template's defaults. The guided steps
// later produce the same files again, adjusted to what discovery finds.

var headerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// capturedHeaders are the headers, of requests or of responses, that the
// instrumentation is asked to record on each span.
func (j *Journey) capturedHeaders(in string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range j.In.Services {
		for _, h := range s.Headers {
			key := strings.ToLower(h.Name)
			if h.In != in || seen[key] || !headerName.MatchString(h.Name) || (h.Kind != HeaderBreakdown && h.Kind != HeaderValue) {
				continue
			}
			seen[key] = true
			out = append(out, h.Name)
		}
	}
	sort.Slice(out, func(a, b int) bool { return strings.ToLower(out[a]) < strings.ToLower(out[b]) })
	return out
}

// headerAttributes are the span attributes a header may arrive as. Agents
// disagree on dashes: Java keeps them, Python turns them into underscores.
func headerAttributes(in, name string) []string {
	lower := strings.ToLower(name)
	out := []string{"http." + in + ".header." + lower}
	if under := strings.ReplaceAll(lower, "-", "_"); under != lower {
		out = append(out, "http."+in+".header."+under)
	}
	return out
}

// headerDimensions is the list the span-metrics connector splits by, written
// so the collector reads it as a list when it expands the variable.
func (j *Journey) headerDimensions() string {
	var dims []string
	for _, in := range []string{"request", "response"} {
		for _, s := range j.In.Services {
			for _, h := range s.Headers {
				if h.In != in || h.Kind != HeaderBreakdown || !headerName.MatchString(h.Name) {
					continue
				}
				for _, a := range headerAttributes(in, h.Name) {
					if d := "{name: " + a + "}"; !contains(dims, d) {
						dims = append(dims, d)
					}
				}
			}
		}
	}
	sort.Strings(dims)
	return "[" + strings.Join(dims, ", ") + "]"
}

// fillVars replaces ${env:NAME} and ${env:NAME:-default} in text nothing
// else will expand: Kubernetes resources and guides.
func fillVars(text string, vars map[string]string) string {
	return envRef.ReplaceAllStringFunc(text, func(m string) string {
		parts := envRef.FindStringSubmatch(m)
		if v, ok := vars[parts[1]]; ok && v != "" {
			return v
		}
		return parts[2]
	})
}

// guides are the instruction files for the chosen components.
func (j *Journey) guides() []*Entry {
	var out []*Entry
	for _, e := range j.Cat.Entries {
		if e.Role == roleGuide && contains(j.In.Components, e.Component) {
			out = append(out, e)
		}
	}
	return out
}

func (j *Journey) guideText(e *Entry) string {
	vars := map[string]string{}
	for _, p := range e.Probes {
		switch {
		case p.Kind != "input":
		case p.Arg == "endpoint":
			vars[p.Var] = j.In.Endpoint
		case p.Arg == "project":
			vars[p.Var] = j.In.Project
		case p.Arg == "env":
			vars[p.Var] = j.In.Env
		}
	}
	var body []string
	for _, line := range strings.Split(e.Body, "\n") {
		if !strings.HasPrefix(line, "#! ") {
			body = append(body, line)
		}
	}
	return strings.TrimSpace(fillVars(strings.Join(body, "\n"), vars)) + "\n"
}

// platforms are the kinds of cluster the project includes.
func (j *Journey) platforms() []string {
	var out []string
	for _, ci := range j.Cat.Components(kindK8s) {
		if ci.Platform && contains(j.In.Components, ci.Name) {
			out = append(out, ci.Name)
		}
	}
	return out
}

// serverComponents are the chosen components that run on servers.
func (j *Journey) serverComponents() []string {
	var out []string
	for _, ci := range j.choosable(kindVM) {
		out = append(out, ci.Name)
	}
	return out
}

// draftTarget is a target as it would be before discovery: everything the
// project asks for, at the template's default locations.
func (j *Journey) draftTarget(name, kind string, components []string) *Target {
	t := &Target{Name: name, Kind: kind, Facts: map[string]string{}, Selected: components}
	t.Vars, _, _ = j.resolveVars(t, components, nil)
	return t
}

type ProcessCommand struct {
	Where string `json:"where"`
	Text  string `json:"text"`
}

// ProcessStep is one stage of the whole setup, from first check to data
// arriving.
type ProcessStep struct {
	Title    string           `json:"title"`
	Text     string           `json:"text"`
	Items    []string         `json:"items,omitempty"`
	Commands []ProcessCommand `json:"commands,omitempty"`
	// Ask names an input that can only be given once this step is done; the
	// steps after it depend on the answer. Waiting marks such a step while
	// the answer is missing.
	Ask     string `json:"ask,omitempty"`
	AskText string `json:"ask_text,omitempty"`
	Waiting bool   `json:"waiting,omitempty"`
	// Manual gives, for each command of this step that goes through
	// otelgenie.sh, the same thing done by hand: what the script runs, without
	// its checking around it. The key is the command's text.
	Manual map[string]string `json:"manual,omitempty"`
}

const projectScript = "scripts/project/otelgenie.sh"

const (
	onServer  = "On the server"
	onKubectl = "On a machine with kubectl"
)

// processSteps lays out every stage for this project. folders maps each kind of
// target to its folder in the package.
func (j *Journey) processSteps(folders map[string]string) []ProcessStep {
	var steps []ProcessStep
	add := func(s ProcessStep) { steps = append(steps, s) }
	platforms := j.platforms()
	servers := folders[kindVM] != ""
	probe := "curl -sS -m 10 -o /dev/null -w '%{http_code}\\n' -X POST -H 'Content-Type: application/json' -d '{}' " + strings.TrimRight(j.In.Endpoint, "/") + "/v1/traces"

	_, scripted := j.Snap.Files[projectScript]
	pkg := "otelgenie-" + slug(j.In.Project) + "-" + slug(j.In.Env)
	where := "the jump server, a machine where kubectl is logged in to the cluster"
	if len(platforms) == 0 {
		where = "the server"
	}
	first := ProcessStep{
		Title: "Download the package and move it to " + map[bool]string{true: "the jump server", false: "the server"}[len(platforms) > 0],
		Text:  "The Download step of the form gives you " + pkg + ".tar.gz: this install guide as README.md, the otelgenie.sh script, and every file the guide refers to. Copy it to " + where + ", and unpack it there. Every command below is run from inside the unpacked folder.",
		Commands: []ProcessCommand{
			{"On your own machine", "scp " + pkg + ".tar.gz YOUR-USER@" + map[bool]string{true: "JUMP-SERVER", false: "SERVER"}[len(platforms) > 0] + ":"},
			{map[bool]string{true: onKubectl, false: onServer}[len(platforms) > 0], "tar xzf " + pkg + ".tar.gz && cd " + pkg + " && ls"},
		},
		Items: []string{"The package holds internal addresses but no passwords or tokens. The access token is typed where the installer runs."},
	}
	// Without a cluster there is no image check to share a step with, so the
	// machine is checked here.
	if scripted && len(platforms) == 0 {
		first.Items = append(first.Items, "Before going further, confirm the server has everything otelgenie.sh needs: bash and the usual shell tools, the package being complete, systemd, and root for the install. The last command checks all of it and names anything missing.")
		first.Commands = append(first.Commands, ProcessCommand{onServer, "bash otelgenie.sh check-requirements"})
	}
	add(first)

	if len(platforms) > 0 {
		stackNote := ""
		if contains(j.In.Components, "app") && len(j.In.Stacks) > 0 {
			var names []string
			for _, s := range j.In.Stacks {
				names = append(names, languages[s])
			}
			stackNote = ", with the auto-instrumentation agents for the application stacks you chose (" + strings.Join(names, ", ") + ") and no others"
		} else if !contains(j.In.Components, "app") {
			stackNote = ". No application is instrumented, so no auto-instrumentation agent is among them"
		}
		inv := ProcessStep{
			Title: "Check the jump server is ready and every container image can be pulled",
			Text:  "One command checks both. First the jump server: bash and the usual shell tools, the package being complete, kubectl (or oc), a current kubectl context, the cluster answering, and a login that may start pods and create namespaces and cluster-wide roles. It prints the context and the cluster address; read them, because every later step acts on that cluster. Then the images: it starts a short-lived pod from each one, reports whether the cluster could pull it, and removes the pods again. The images are only tried once the first part passes.",
			Items: []string{"If it is the wrong cluster, switch with kubectl config use-context and run it again.", "An image that cannot be pulled has to be copied into a registry the cluster can reach before anything is installed. These are all the images this setup uses" + stackNote + ":"},
		}
		for _, im := range j.images() {
			if !j.needsImage(im[0]) {
				continue
			}
			if im[1] == "" {
				inv.Items = append(inv.Items, im[0])
			} else {
				inv.Items = append(inv.Items, im[1]+" (a copy of "+im[0]+")")
			}
		}
		inv.Items = append(inv.Items, j.Profile.curlImage()+" (only for the connectivity check)")
		if scripted {
			inv.Commands = append(inv.Commands, ProcessCommand{onKubectl, "bash otelgenie.sh check-requirements"})
		} else {
			inv.Commands = append(inv.Commands, ProcessCommand{onKubectl, "cat " + folders[platforms[0]] + "/images.txt"})
		}
		add(inv)
	}

	check := ProcessStep{
		Title: "Check the destination can be reached",
		Text:  "Everything is sent to " + j.In.Endpoint + ". Before installing anything, confirm that address answers from where the collectors will run. Any HTTP status, even 401, means the network path is open; a timeout or a certificate error has to be fixed first.",
	}
	viaIngress := j.serversBesideCluster()
	if viaIngress {
		check.Text = "Only the cluster's collector sends to " + j.In.Endpoint + ". The collectors on the cluster's nodes hand their data to it, and servers outside the cluster reach it through an Ingress that a later step creates. Before installing anything, confirm the destination answers from inside the cluster. Any HTTP status, even 401, means the network path is open; a timeout or a certificate error has to be fixed first."
	} else if servers && scripted {
		check.Commands = append(check.Commands, ProcessCommand{onServer, "bash otelgenie.sh check-connectivity"})
	} else if servers {
		check.Commands = append(check.Commands, ProcessCommand{onServer, probe})
	}
	raw := "kubectl run otelgenie-check --rm -i --restart=Never --image=" + j.Profile.curlImage() + " --command -- " + probe
	switch {
	case len(platforms) > 0 && scripted:
		check.Items = append(check.Items,
			"On the jump server the script runs the check from a pod, since pods may be routed differently from the jump server itself. The image is the one named in the one-time setup; it only needs curl in it.",
			"What it runs, if you want to do it by hand: "+raw)
		check.Commands = append(check.Commands, ProcessCommand{onKubectl, "bash otelgenie.sh check-connectivity"})
	case len(platforms) > 0:
		check.Items = append(check.Items, "In a cluster, run the check from a pod, since pods may be routed differently from your own machine. The image is the one named in the one-time setup; it only needs curl in it.")
		check.Commands = append(check.Commands, ProcessCommand{onKubectl, raw})
	}
	if strings.TrimSpace(j.Profile.CABundle) != "" && !scripted {
		check.Items = append(check.Items, "The destination uses your private certificate authority. The package carries it as ca-bundle.pem; add --cacert ca-bundle.pem to the curl command.")
	}
	add(check)

	if len(platforms) > 0 {
		pre := ProcessStep{
			Title: "Check what the cluster already has",
			Text:  "The collectors read from kube-state-metrics and the metrics server, and the operator needs cert-manager. discover.sh reports all of this without changing anything.",
			Commands: []ProcessCommand{
				{onKubectl, "kubectl config current-context"},
				{onKubectl, "bash " + folders[platforms[0]] + "/discover.sh"},
				{onKubectl, "kubectl get crd certificates.cert-manager.io && kubectl get svc -A | grep -E 'kube-state-metrics|metrics-server'"},
			},
			Items: []string{"kube-state-metrics and the metrics server are not installed by this package. If one is missing, its metrics stay empty until the platform team adds it."},
		}
		// A template that says how to install them turns this into the step
		// that does so, each one checked first with kubectl alone.
		var addons []Artifact
		for _, a := range j.Man.Artifacts {
			if a.Kind == "addon" && a.Name != "" {
				addons = append(addons, a)
			}
		}
		// Which of them this project has to install depends on the kinds of
		// cluster chosen: some come with an addon, some do not need it.
		var missing, given []Artifact
		for _, a := range addons {
			needed := false
			for _, p := range platforms {
				needed = needed || (!contains(a.SkipOn, p) && !contains(a.BuiltinOn, p))
			}
			if needed {
				missing = append(missing, a)
			} else {
				given = append(given, a)
			}
		}
		list := func(as []Artifact) string {
			var names []string
			for _, a := range as {
				names = append(names, a.Name)
			}
			if len(names) < 2 {
				return strings.Join(names, "")
			}
			return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
		}
		on := strings.Join(platforms, ", ")
		if len(addons) > 0 {
			pre = ProcessStep{Title: "Install " + list(missing)}
			pre.Text = "The collectors read from kube-state-metrics and the metrics server, and the operator needs cert-manager. Which of them has to be installed depends on the kind of cluster, and a cluster may already have one, so nothing is installed blindly: the script first reports what is there, and then installs only what is missing."
			if len(missing) == 0 {
				pre.Title = "Check the cluster's prerequisites"
				pre.Text = "The collectors read from kube-state-metrics and the metrics server, and the operator needs cert-manager. On " + on + " nothing of this has to be installed; the script only confirms what the cluster provides is there."
			}
			for _, a := range given {
				var builtin []string
				for _, p := range platforms {
					if contains(a.BuiltinOn, p) {
						builtin = append(builtin, p)
					}
				}
				if len(builtin) > 0 {
					pre.Items = append(pre.Items, a.Name+": comes with "+strings.Join(builtin, ", ")+", so it is not installed here. The script only confirms it is there.")
				} else {
					pre.Items = append(pre.Items, a.Name+": not needed on "+on+".")
				}
			}
			if scripted {
				pre.Commands = append(pre.Commands, ProcessCommand{onKubectl + ": see what is already there (changes nothing)", "bash otelgenie.sh check-prerequisites"})
				if len(missing) > 0 {
					pre.Items = append(pre.Items, "The second command installs what the first reported missing. For each one it runs the detection again; if nothing is found it shows the images the file will pull, asks you to confirm, applies the file, and waits until the detection succeeds.")
					pre.Commands = append(pre.Commands, ProcessCommand{onKubectl + ": install what is missing", "bash otelgenie.sh install-prerequisites"})
				}
			}
			for _, a := range missing {
				how, flag := "download "+a.URL+" and save it as "+a.File, "-f"
				if !strings.HasSuffix(a.File, ".yaml") && !strings.HasSuffix(a.File, ".yml") {
					how, flag = "copy the folder "+a.File+" from "+a.URL, "-k"
				}
				item := a.Name + ": if it is missing, " + how + " on a machine with internet access, and put it next to otelgenie.sh on the jump server."
				var except []string
				for _, p := range platforms {
					if contains(a.SkipOn, p) || contains(a.BuiltinOn, p) {
						except = append(except, p)
					}
				}
				if len(except) > 0 {
					item += " Not installed on " + strings.Join(except, ", ") + "."
				}
				if a.Note != "" {
					item += " " + a.Note
				}
				pre.Items = append(pre.Items, item)
				if !scripted {
					if a.Check != "" {
						pre.Commands = append(pre.Commands, ProcessCommand{"Check whether " + a.Name + " is already installed", a.Check})
					}
					pre.Commands = append(pre.Commands, ProcessCommand{"Install " + a.Name + ", only if it is missing", "kubectl apply " + flag + " " + a.File})
				}
			}
			if j.Profile.ImageMethod != "none" && len(missing) > 0 {
				pre.Items = append(pre.Items, "These files name public images. Images come through "+j.Profile.Registry+", so change each image: line to its address there before running the install; the script shows the images it is about to use.")
			}
		}
		add(pre)

		op := ProcessStep{Title: "Install the OpenTelemetry Operator", Text: "The operator is what creates and looks after the collectors and injects the agents into application pods."}
		a := j.operatorArtifact()
		byPackage := a != nil && (len(platforms) > 1 || !contains(platforms, "ocp"))
		if contains(platforms, "ocp") {
			op.Items = append(op.Items, "On OpenShift: a cluster administrator installs \"Red Hat build of OpenTelemetry\" from OperatorHub, for all namespaces. The script does not install it there; it tells you so and checks for it.")
		}
		if byPackage {
			op.Items = append(op.Items,
				"On GKE and EKS: cert-manager must be installed first; the script refuses to go on without it.",
				"Download "+a.URL+" on a machine with internet access, save it as "+a.File+" and put it in the cluster folder.")
		}
		if j.Profile.ImageMethod != "none" {
			op.Items = append(op.Items, "Images come through "+j.Profile.Registry+". The script points the operator's file at the addresses in the second column of images.txt before applying it.")
		}
		validate := ProcessStep{
			Title: "Validate the operator",
			Text:  "The operator is ready when its deployment is running and its two resource types exist.",
			Items: []string{"On OpenShift the operator runs in the namespace openshift-opentelemetry-operator; the script finds it wherever it is."},
		}
		if scripted {
			op.Items = append([]string{"The script first looks whether the operator is already running and, if so, changes nothing. Otherwise it shows the images it will pull, asks you to confirm, applies the file and waits until the operator is running."}, op.Items...)
			op.Commands = append(op.Commands, ProcessCommand{onKubectl, "bash otelgenie.sh install-operator"})
			validate.Commands = append(validate.Commands, ProcessCommand{onKubectl, "bash otelgenie.sh check-operator"})
		} else {
			if byPackage {
				op.Commands = append(op.Commands, ProcessCommand{onKubectl, "kubectl apply --server-side -f " + a.File})
			}
			validate.Commands = append(validate.Commands,
				ProcessCommand{onKubectl, "kubectl -n opentelemetry-operator-system rollout status deployment/opentelemetry-operator-controller-manager --timeout=180s"},
				ProcessCommand{onKubectl, "kubectl get crd opentelemetrycollectors.opentelemetry.io instrumentations.opentelemetry.io"})
		}
		add(op)
		add(validate)

		install := ProcessStep{
			Title: "Install the collectors in the cluster",
			Text:  "apply.sh asks for the access token (typing is hidden), stores settings and secrets in the Secret otelgenie-env, applies the resources in manifests/ and waits for the collectors. Everything goes in the namespace " + k8sNamespace + ": a DaemonSet with one collector per node, and a Deployment with one collector for the cluster. Applications send to the collector on their node, the node collectors send to the cluster's collector, and only that one sends to the destination.",
		}
		if scripted {
			install.Items = append(install.Items, "The script checks the operator is running, tells you what it is about to apply and asks you to confirm, then runs the cluster folder's apply.sh. The second command confirms everything it created is there and ready.")
			install.Commands = append(install.Commands,
				ProcessCommand{onKubectl, "bash otelgenie.sh install-collector"},
				ProcessCommand{onKubectl, "bash otelgenie.sh check-collector"})
			if len(platforms) > 1 {
				install.Items = append(install.Items, "This project has several kinds of cluster. On each one say which folder is meant, for example: CLUSTER_FOLDER="+folders[platforms[0]]+" bash otelgenie.sh install-collector")
			}
		} else {
			for _, p := range platforms {
				install.Commands = append(install.Commands, ProcessCommand{onKubectl + " (" + p + ")", "cd " + folders[p] + " && bash apply.sh"})
			}
		}
		add(install)

		if viaIngress {
			ing := ProcessStep{
				Title:   "Create the Ingress for servers outside the cluster",
				Text:    "Servers outside the cluster do not send to the destination. They reach the cluster's collector through an Ingress named otelgenie-gateway in the namespace " + k8sNamespace + ", which only passes OTLP over HTTP (/v1/...) to that collector. The collector accepts only senders that present the access token. apply.sh has already applied the file; the first command is what it ran, and is safe to run again.",
				Items:   []string{"The Ingress uses the cluster's default ingress class. If yours needs a class name, a host name or a certificate, add them to the file and apply it again.", "Wait until the second command shows an ADDRESS. A 401 from the third command means the Ingress reaches the collector, since the check carries no token."},
				Ask:     "ingress",
				AskText: "What address did the Ingress get? Give it as servers will use it, for example http://203.0.113.10 or https://otel.apps.example.com. The steps below are filled in from it.",
			}
			if scripted {
				ing.Text = "Servers outside the cluster do not send to the destination. They reach the cluster's collector through an Ingress named otelgenie-gateway in the namespace " + k8sNamespace + ", which only passes OTLP over HTTP (/v1/...) to that collector. The collector accepts only senders that present the access token. The Ingress was created with the collectors in the previous step; this command shows it, the address it was given, and whether that address reaches the collector."
				ing.Items = []string{"The Ingress uses the cluster's default ingress class. If yours needs a class name, a host name or a certificate, add them to the ingress file in the cluster folder's manifests and run install-collector again.", "Run the command until it reports an address. It then tries the address: a 401 means the Ingress reaches the collector, since the check carries no token."}
				ing.Commands = append(ing.Commands, ProcessCommand{onKubectl, "bash otelgenie.sh check-ingress"})
			} else {
				for _, p := range platforms {
					ing.Commands = append(ing.Commands, ProcessCommand{onKubectl + " (" + p + ")", "kubectl apply -f " + folders[p] + "/manifests/10-" + p + "-ingress.yaml"})
				}
				ing.Commands = append(ing.Commands,
					ProcessCommand{onKubectl, "kubectl -n " + k8sNamespace + " get ingress otelgenie-gateway"},
					ProcessCommand{onKubectl, "curl -sS -m 10 -o /dev/null -w '%{http_code}\\n' -X POST -H 'Content-Type: application/json' -d '{}' http://ADDRESS/v1/traces"})
			}
			add(ing)
		}
	}

	waiting := viaIngress && j.In.Ingress == ""
	if servers || waiting {
		set := j.Man.Settings
		srv := ProcessStep{
			Title: "Install the collector on each server",
			Text: fmt.Sprintf("Copy the server folder to the server. discover.sh reports what is there; apply.sh installs the configuration as the systemd service otelgenie-otelcol and asks for the access token. The service runs as the user %s, which apply.sh creates if it is missing, and is limited to %d MB of memory and %d%% of one CPU. The collector package (otelcol-contrib %s) must be installed first.",
				set.ServiceUser, set.MemoryMaxMB, set.CPUQuotaPercent, j.Man.CollectorVersion),
		}
		switch {
		case waiting:
			srv.Waiting = true
			srv.Items = append(srv.Items, "The server's files are written once the Ingress address is known. Give it in the step above.")
		case viaIngress:
			srv.Items = append(srv.Items, "Servers send to the cluster's collector through "+j.In.Ingress+", not to the destination. The connectivity check tries that address: 401 means the server can reach it, since the check carries no token.")
		}
		switch {
		case waiting:
		case scripted:
			srv.Text = fmt.Sprintf("Copy otelgenie.sh, project.env, packages.txt and the server folder to the server. The script checks the server, then installs the configuration as the systemd service otelgenie-otelcol and asks for the access token. The service runs as the user %s, which is created if it is missing, and is limited to %d MB of memory and %d%% of one CPU. If the collector itself (otelcol-contrib %s) is not on the server yet, the install command installs it first.",
				set.ServiceUser, set.MemoryMaxMB, set.CPUQuotaPercent, j.Man.CollectorVersion)
			if j.Profile.PackageMethod == "repo" {
				srv.Items = append(srv.Items, "The collector package comes from your internal package repository ("+j.Profile.PackageRepoURL+"), unless its file is next to the script.")
			} else {
				var names []string
				for _, a := range j.Man.Artifacts {
					if a.Kind == "package" {
						names = append(names, a.File+" ("+a.OS+", "+a.Arch+") from "+a.URL)
					}
				}
				srv.Items = append(srv.Items, "The collector package is carried in by hand. Put the one for the server's system next to otelgenie.sh; the script picks the right one and tells you if it is missing. They are: "+strings.Join(names, "; ")+".")
			}
			srv.Items = append(srv.Items, "A collector that is already installed is left as it is.")
			srv.Commands = append(srv.Commands,
				ProcessCommand{onServer + ": the address it sends to answers", "bash otelgenie.sh check-connectivity"},
				ProcessCommand{onServer + ": what is there before installing", "bash otelgenie.sh check-before-install"},
				ProcessCommand{onServer + ": install", "sudo bash otelgenie.sh install-collector"},
				ProcessCommand{onServer + ": the service is running as it should", "bash otelgenie.sh check-collector"})
		default:
			if viaIngress {
				srv.Commands = append(srv.Commands, ProcessCommand{onServer, strings.Replace(probe, strings.TrimRight(j.In.Endpoint, "/"), j.In.Ingress, 1)})
			}
			srv.Commands = append(srv.Commands,
				ProcessCommand{onServer, "bash " + folders[kindVM] + "/discover.sh"},
				ProcessCommand{onServer, "cd " + folders[kindVM] + " && sudo bash apply.sh"})
		}
		add(srv)
	}

	flowing := ProcessStep{Title: "Validate that data is flowing", Text: "Each collector keeps its own counters: what every receiver took in, and what was sent on. Wait a minute after installing before reading them."}
	if scripted {
		flowing.Text += " The script reads them and says, for each collector, how much came in, how much went out and whether anything was lost."
		if len(platforms) > 0 {
			flowing.Commands = append(flowing.Commands, ProcessCommand{onKubectl, "bash otelgenie.sh check-data-flow"})
		}
		if servers {
			flowing.Commands = append(flowing.Commands, ProcessCommand{onServer, "bash otelgenie.sh check-data-flow"})
		}
	} else {
		for _, p := range platforms {
			flowing.Commands = append(flowing.Commands,
				ProcessCommand{onKubectl + " (" + p + ")", "cd " + folders[p] + " && bash verify.sh"},
				ProcessCommand{onKubectl + " (" + p + ")", "kubectl -n " + k8sNamespace + " get opentelemetrycollectors,pods"})
		}
		if servers {
			flowing.Commands = append(flowing.Commands, ProcessCommand{onServer, "cd " + folders[kindVM] + " && sudo bash verify.sh"})
		}
	}
	add(flowing)

	if len(j.In.Services) > 0 && contains(j.In.Components, "app") {
		svc := ProcessStep{Title: "Instrument the microservices", Text: "Nothing arrives from a service until its owner switches instrumentation on."}
		req, resp := strings.Join(j.capturedHeaders("request"), ","), strings.Join(j.capturedHeaders("response"), ",")
		if len(platforms) > 0 {
			svc.Items = append(svc.Items,
				"In a cluster: each service's pod template needs one annotation naming its language (not a label), and the pods have to restart. The service name is taken from the workload name unless OTEL_SERVICE_NAME is set.",
				"The headers chosen from the API descriptions are captured automatically.")
			var goServices []string
			for _, s := range j.In.Services {
				lang := languages[s.language()]
				snippet, pairs := "annotations:", []string(nil)
				for _, a := range s.annotations() {
					snippet += "\n  " + a[0] + ": \"" + a[1] + "\""
					pairs = append(pairs, "\""+a[0]+"\":\""+a[1]+"\"")
				}
				if s.language() == "go" {
					goServices = append(goServices, s.Name)
				}
				switch keys := s.chartKeys(); {
				case len(keys) > 0:
					file := serviceFolder(s) + "/values-otel.yaml"
					svc.Items = append(svc.Items, fmt.Sprintf("%s (%s): its chart %s passes %s to the pods' annotations, so %s sets the annotation there. Upgrading the release restarts the pods.", s.Name, lang, s.Chart.Name, strings.Join(keys, ", "), file))
					svc.Commands = append(svc.Commands, ProcessCommand{onKubectl + " (" + s.Name + ")", "helm upgrade YOUR-RELEASE PATH-TO-CHART -n YOUR-NAMESPACE --reuse-values -f " + file})
				case s.Chart != nil && len(s.Chart.Workloads) > 0:
					w := s.Chart.Workloads[0]
					svc.Items = append(svc.Items, fmt.Sprintf("%s (%s): its chart %s has no value that reaches the pods' annotations. Add the annotation under spec.template.metadata in %s, then upgrade the release.", s.Name, lang, s.Chart.Name, w.File))
					svc.Commands = append(svc.Commands, ProcessCommand{"In " + w.File + " of the " + s.Chart.Name + " chart, under spec.template.metadata", snippet})
				default:
					why := "no chart was given"
					if s.Chart != nil {
						why = "its chart " + s.Chart.Name + " has no workload template that could be read"
					}
					svc.Items = append(svc.Items, fmt.Sprintf("%s (%s): %s, so the annotation is put on the workload directly. A later helm upgrade will remove it again unless the chart sets it too.", s.Name, lang, why))
					if scripted {
						cmd := "bash otelgenie.sh instrument-workload YOUR-NAMESPACE deployment/" + s.Name + " " + s.language()
						if s.language() == "go" {
							cmd += " " + goTargetPlaceholder
						}
						svc.Commands = append(svc.Commands, ProcessCommand{onKubectl + " (" + s.Name + ")", cmd})
						continue
					}
					svc.Commands = append(svc.Commands, ProcessCommand{onKubectl + " (" + s.Name + ")", "kubectl -n YOUR-NAMESPACE patch deployment " + s.Name + " --type merge -p '{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{" + strings.Join(pairs, ",") + "}}}}}'"})
				}
			}
			if len(goServices) > 0 || contains(j.In.Stacks, "go") {
				svc.Items = append(svc.Items,
					"Go services are instrumented from outside the process. The operator must be running with Go instrumentation switched on (its --enable-go-instrumentation setting), and the namespace must allow the privileged container it adds to each pod.",
					"Each Go service also needs "+goTargetAnnotation+" set to the path of its binary inside the container; replace "+goTargetPlaceholder+" where it appears. Headers are not captured for Go services.")
			}
		}
		if servers {
			env := "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 OTEL_SERVICE_NAME=" + j.In.Services[0].Name
			if req != "" {
				env += " OTEL_INSTRUMENTATION_HTTP_SERVER_CAPTURE_REQUEST_HEADERS=" + req
			}
			if resp != "" {
				env += " OTEL_INSTRUMENTATION_HTTP_SERVER_CAPTURE_RESPONSE_HEADERS=" + resp
			}
			svc.Items = append(svc.Items, "On a server: each service runs with its language's OpenTelemetry agent and these settings (shown for a Java service). Python uses OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_SERVER_REQUEST and ..._SERVER_RESPONSE for the header lists.")
			svc.Commands = append(svc.Commands, ProcessCommand{"In the service's environment", env})
		}
		var names []string
		for _, s := range j.In.Services {
			names = append(names, s.Name)
		}
		svc.Items = append(svc.Items, "Services in this project: "+strings.Join(names, ", ")+".")
		add(svc)
	}

	if _, ok := j.Snap.Files[projectScript]; ok && len(platforms) > 0 {
		add(ProcessStep{
			Title: "Validate everything on Kubernetes",
			Text:  "One last pass over the whole cluster side, from a jump server where kubectl points at the cluster: the destination answers from a pod, the prerequisites and the operator are running, every collector pod is ready and not restarting, each collector is taking data in and sending it on with nothing lost, the node collectors can reach the cluster's collector, the Ingress answers, and application pods carry the agent. It changes nothing and ends with a count of what passed and failed.",
			Items: []string{
				"It repeats the checks of the earlier steps in one pass, so a clean result here means the whole cluster side is in order.",
			},
			Commands: []ProcessCommand{{onKubectl, "bash otelgenie.sh validate-everything"}},
		})
	}

	if gs := j.guides(); len(gs) > 0 {
		front := ProcessStep{Title: "Instrument the frontend applications", Text: "Frontend applications send from the user's device, so they are set up in their own code. The package has a guide for each."}
		for _, e := range gs {
			front.Items = append(front.Items, e.Title+": guides/"+e.Component+".md")
		}
		add(front)
	}
	for i := range steps {
		for _, c := range steps[i].Commands {
			if m := j.byHand(c, folders, probe); m != "" {
				if steps[i].Manual == nil {
					steps[i].Manual = map[string]string{}
				}
				steps[i].Manual[c.Text] = m
			}
		}
	}
	return steps
}

// byHand gives the commands a person would type instead of one otelgenie.sh
// command. It returns nothing for a command that is not the script's.
func (j *Journey) byHand(c ProcessCommand, folders map[string]string, probe string) string {
	_, rest, ok := strings.Cut(c.Text, "otelgenie.sh ")
	if !ok {
		return ""
	}
	args := strings.Fields(rest)
	onVM := strings.HasPrefix(c.Where, onServer)
	platforms := j.platforms()
	ns := k8sNamespace
	first := ""
	if len(platforms) > 0 {
		first = folders[platforms[0]]
	}
	lines := func(l ...string) string { return strings.Join(l, "\n") }
	var addons []Artifact
	for _, a := range j.Man.Artifacts {
		if a.Kind != "addon" || a.Name == "" {
			continue
		}
		for _, p := range platforms {
			if !contains(a.SkipOn, p) && !contains(a.BuiltinOn, p) {
				addons = append(addons, a)
				break
			}
		}
	}
	serverProbe := strings.Replace(probe, strings.TrimRight(j.In.Endpoint, "/"), strings.TrimRight(j.sendsTo(kindVM), "/"), 1)

	switch args[0] {
	case "check-requirements":
		if onVM {
			return lines("command -v systemctl curl awk sed", "id -u    # 0 means root; otherwise sudo is needed for the install", "ls otelgenie.sh project.env packages.txt "+folders[kindVM])
		}
		return lines(
			"kubectl version --client",
			"kubectl config current-context    # the cluster every later step acts on",
			"kubectl get --raw /version",
			"kubectl auth can-i create pods",
			"kubectl auth can-i create namespaces && kubectl auth can-i create clusterrolebindings",
			"# Then for each image in "+first+"/images.txt (the second address, where a line has two):",
			"kubectl run otelgenie-image-check --image=IMAGE --restart=Never --command -- true",
			"kubectl get pod otelgenie-image-check    # ErrImagePull or ImagePullBackOff: the cluster cannot pull it",
			"kubectl delete pod otelgenie-image-check")
	case "check-connectivity":
		if onVM {
			return serverProbe
		}
		return "kubectl run otelgenie-check --rm -i --restart=Never --image=" + j.Profile.curlImage() + " --command -- " + probe
	case "check-prerequisites":
		var out []string
		for _, a := range addons {
			if a.Check != "" {
				out = append(out, "# "+a.Name, a.Check)
			}
		}
		if len(out) == 0 {
			return "# Nothing has to be installed on " + strings.Join(platforms, ", ") + "."
		}
		return lines(out...)
	case "install-prerequisites":
		var out []string
		for _, a := range addons {
			flag := "-f"
			if !strings.HasSuffix(a.File, ".yaml") && !strings.HasSuffix(a.File, ".yml") {
				flag = "-k"
			}
			out = append(out, "# "+a.Name+", only if its check found nothing", "kubectl apply "+flag+" "+a.File)
		}
		return lines(out...)
	case "install-operator":
		a := j.operatorArtifact()
		if a == nil {
			return "# Install the operator from OperatorHub."
		}
		out := []string{"kubectl get crd opentelemetrycollectors.opentelemetry.io    # if this finds it, the operator is already installed"}
		if j.Profile.ImageMethod != "none" {
			out = append(out, "# First change the image addresses in "+a.File+" to those in the second column of images.txt.")
		}
		out = append(out, "kubectl apply --server-side -f "+first+"/"+a.File)
		if contains(platforms, "ocp") {
			out = append(out, "# On OpenShift, install \"Red Hat build of OpenTelemetry\" from OperatorHub instead.")
		}
		return lines(out...)
	case "check-operator":
		return lines(
			"kubectl get deployments -A -l app.kubernetes.io/name=opentelemetry-operator",
			"kubectl -n opentelemetry-operator-system rollout status deployment/opentelemetry-operator-controller-manager --timeout=180s",
			"kubectl get crd opentelemetrycollectors.opentelemetry.io instrumentations.opentelemetry.io")
	case "check-before-install":
		if onVM {
			return "bash " + folders[kindVM] + "/discover.sh"
		}
		return lines("kubectl get nodes", "bash "+first+"/discover.sh")
	case "install-collector":
		if onVM {
			pkg := "sudo rpm -Uvh otelcol-contrib_*.rpm    # or: sudo dpkg -i otelcol-contrib_*.deb"
			if j.Profile.PackageMethod == "repo" {
				pkg = "sudo dnf install -y otelcol-contrib    # or: sudo apt-get install -y otelcol-contrib"
			}
			return lines("command -v otelcol-contrib    # if this finds it, skip the next line", pkg, "cd "+folders[kindVM]+" && sudo bash apply.sh")
		}
		var out []string
		for _, p := range platforms {
			out = append(out, "cd "+folders[p]+" && bash apply.sh    # on the "+p+" cluster")
		}
		return lines(out...)
	case "check-collector":
		if onVM {
			return lines("systemctl status otelgenie-otelcol --no-pager", "systemctl show otelgenie-otelcol -p User -p MemoryMax -p CPUQuotaPerSecUSec", "curl -fsS http://127.0.0.1:13133/")
		}
		return lines(
			"kubectl -n "+ns+" get opentelemetrycollectors,daemonsets,deployments,pods",
			"kubectl -n "+ns+" get secret otelgenie-env",
			"kubectl -n "+ns+" get instrumentations,ingress")
	case "check-ingress":
		return lines(
			"kubectl -n "+ns+" get ingress otelgenie-gateway    # wait for an ADDRESS",
			"curl -sS -m 10 -o /dev/null -w '%{http_code}\\n' -X POST -H 'Content-Type: application/json' -d '{}' http://ADDRESS/v1/traces    # 401 is the right answer")
	case "check-data-flow":
		if onVM {
			return lines("cd "+folders[kindVM]+" && sudo bash verify.sh", "curl -s http://127.0.0.1:8888/metrics | grep -E '^otelcol_(receiver_accepted|exporter_sent|exporter_send_failed)_'")
		}
		return lines(
			"cd "+first+" && bash verify.sh",
			"# or, for one collector pod:",
			"kubectl get --raw /api/v1/namespaces/"+ns+"/pods/POD:8888/proxy/metrics | grep -E '^otelcol_(receiver_accepted|exporter_sent|exporter_send_failed)_'")
	case "instrument-workload":
		if len(args) < 4 {
			return ""
		}
		pairs := "\"" + injectPrefix + args[3] + "\":\"" + ns + "/otelgenie\""
		if len(args) > 4 {
			pairs += ",\"" + goTargetAnnotation + "\":\"" + args[4] + "\""
		}
		return lines(
			"kubectl -n "+args[1]+" patch "+args[2]+" --type merge -p '{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{"+pairs+"}}}}}'",
			"kubectl -n "+args[1]+" rollout status "+args[2])
	case "validate-everything":
		return lines(
			"kubectl run otelgenie-check --rm -i --restart=Never --image="+j.Profile.curlImage()+" --command -- "+probe,
			"kubectl get deployments -A -l app.kubernetes.io/name=opentelemetry-operator",
			"kubectl -n "+ns+" get opentelemetrycollectors,daemonsets,deployments,pods,instrumentations,ingress",
			"kubectl -n "+ns+" get endpoints otelgenie-cluster-collector",
			"kubectl -n "+ns+" logs -l app.kubernetes.io/managed-by=opentelemetry-operator --since=10m | grep -iE 'error|Exporting failed'",
			"cd "+first+" && bash verify.sh",
			"kubectl get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name} {.spec.initContainers[*].name}{\"\\n\"}{end}' | grep opentelemetry-auto-instrumentation")
	}
	return ""
}

// readme is the README.md at the top of the package.
func (j *Journey) readme(steps []ProcessStep, folders map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Telemetry setup for %s / %s\n\n", j.In.Project, j.In.Env)
	through := "the destination directly"
	if j.serversBesideCluster() {
		through = "the cluster's collector, at an Ingress address not given yet"
		if j.In.Ingress != "" {
			through = "the cluster's collector, at " + j.In.Ingress
		}
	}
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Project | %s |\n| Environment | %s |\n| Destination | %s |\n| Servers send through | %s |\n| Enabled | %s |\n| Components | %s |\n| Template | %s at %s |\n| Setup fingerprint | %s |\n\n",
		j.In.Project, j.In.Env, j.In.Endpoint, through, strings.Join(j.In.Signals, ", "), strings.Join(j.In.Components, ", "),
		j.Snap.Repo, j.Snap.Commit, ComputeFingerprint(j.Profile, j.Snap, j.Man).Code)
	b.WriteString("The access token for the destination is not in this package. apply.sh asks for it where it runs.\n\n")

	b.WriteString("## What is in this package\n\n")
	for _, k := range sortedKeys(folders) {
		what := "files for a " + k + " cluster: resources in manifests/, images.txt, and the discover, apply, verify and rollback scripts"
		if k == kindVM {
			what = "files for a server: collector configuration in conf.d/, and the discover, apply, verify and rollback scripts"
		}
		fmt.Fprintf(&b, "- `%s/`: %s\n", folders[k], what)
	}
	for _, s := range j.In.Services {
		if len(s.chartKeys()) > 0 {
			fmt.Fprintf(&b, "- `%s/values-otel.yaml`: values for the %s chart that switch instrumentation on for %s\n", serviceFolder(s), s.Chart.Name, s.Name)
		}
	}
	for _, e := range j.guides() {
		fmt.Fprintf(&b, "- `guides/%s.md`: %s\n", e.Component, e.Title)
	}
	if _, ok := j.Snap.Files[projectScript]; ok {
		b.WriteString("- `otelgenie.sh`: one script for the checks (it reads `project.env` and `prerequisites.txt`); see below\n")
	}
	b.WriteString("\nThese files use the template's usual locations. The guided setup in otelgenie looks at each server or cluster first and adjusts them to what it finds.\n\n")

	if len(j.In.Services) > 0 {
		b.WriteString("## Microservices and metrics from their headers\n\n")
		b.WriteString("Every service gets request count, error count and duration per operation. The headers below were found in the API descriptions and add to that.\n\n")
		words := map[string]string{
			HeaderBreakdown: "Request metrics are split by it",
			HeaderValue:     "Recorded on every request; not a metric of its own",
		}
		for _, s := range j.In.Services {
			fmt.Fprintf(&b, "### %s\n\n", s.Name)
			if len(s.Headers) == 0 {
				b.WriteString("No headers chosen.\n\n")
				continue
			}
			b.WriteString("| Header | On | What it gives | Span attribute |\n|---|---|---|---|\n")
			for _, h := range s.Headers {
				fmt.Fprintf(&b, "| %s | %s | %s | `%s` |\n", h.Name, h.In, words[h.Kind], headerAttributes(h.In, h.Name)[0])
			}
			b.WriteString("\n")
		}
		b.WriteString("The metrics are named `traces.span.metrics.calls` and `traces.span.metrics.duration`, with the service, the operation, the status and each chosen header as attributes.\n\n")
	}

	if _, ok := j.Snap.Files[projectScript]; ok {
		b.WriteString("## One script for the checks\n\n")
		rows := [][3]string{
			{"bash otelgenie.sh check-requirements", "Tools, the package, the kubectl context, the connection, what the login may do, and that the cluster can pull every image", "Tools, the package, systemd and root"},
			{"bash otelgenie.sh check-connectivity", "The destination answers from a pod", "The address this server sends to answers"},
			{"bash otelgenie.sh check-prerequisites", "Which of kube-state-metrics, the metrics server and cert-manager this kind of cluster needs, and which are already there", "Not used"},
			{"bash otelgenie.sh install-prerequisites", "Installs those that are needed and not already there", "Not used"},
			{"bash otelgenie.sh install-operator", "Installs the OpenTelemetry Operator, unless it is already running", "Not used"},
			{"bash otelgenie.sh check-operator", "The operator is running and its resource types exist", "Not used"},
			{"bash otelgenie.sh check-before-install", "Nodes, permissions, prerequisites, what is already installed", "systemd, the collector package, the service user"},
			{"bash otelgenie.sh install-collector", "Installs the collectors and the resources around them into the cluster", "Installs the collector package if it is missing, then the collector as a systemd service (needs sudo)"},
			{"bash otelgenie.sh check-collector", "Prerequisites, namespace, collectors and the resources around them", "Service active, its user, its memory and CPU limits"},
			{"bash otelgenie.sh check-ingress", "The Ingress servers send through, its address, and whether it reaches the collector", "Not used"},
			{"bash otelgenie.sh instrument-workload NAMESPACE WORKLOAD LANGUAGE", "Switches auto-instrumentation on for one workload", "Not used"},
			{"bash otelgenie.sh check-data-flow", "Each collector takes data in and sends it on", "This server's collector takes data in and sends it on"},
			{"bash otelgenie.sh validate-everything", "Every check above, plus pods, logs and the way in from outside", "The server checks above"},
			{"bash otelgenie.sh run-all", "Every check in order; installs nothing", "Everything in order, including the install"},
		}
		if len(j.platforms()) == 0 {
			b.WriteString("`otelgenie.sh` checks the server it runs on. It only reads, except for `install-collector`.\n\n| Command | What it does |\n|---|---|\n")
			for _, r := range rows {
				fmt.Fprintf(&b, "| `%s` | %s |\n", r[0], r[2])
			}
		} else {
			b.WriteString("`otelgenie.sh` works out where it is running. On a jump server where kubectl points at the cluster it uses kubectl and checks the cluster; anywhere else it works on the server it is on. The `check-` commands only read. The `install-` commands and `instrument-workload` look first, do only what is missing and, in a cluster, ask before they act.\n\n")
			b.WriteString("| Command | On a jump server with kubectl | On a server |\n|---|---|---|\n")
			for _, r := range rows {
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", r[0], r[1], r[2])
			}
		}
		b.WriteString("\nTo use it on a server, copy `otelgenie.sh`, `project.env`, `packages.txt` and the `server` folder there together.\n\n")
	}

	b.WriteString("## Install guide\n\n")
	for i, s := range steps {
		fmt.Fprintf(&b, "### %d. %s\n\n%s\n\n", i+1, s.Title, s.Text)
		for _, it := range s.Items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
		if len(s.Items) > 0 {
			b.WriteString("\n")
		}
		for _, c := range s.Commands {
			fmt.Fprintf(&b, "%s:\n\n```sh\n%s\n```\n\n", c.Where, c.Text)
			if m := s.Manual[c.Text]; m != "" {
				fmt.Fprintf(&b, "<details><summary>The same by hand, without otelgenie.sh</summary>\n\n```sh\n%s\n```\n\n</details>\n\n", m)
			}
		}
	}
	b.WriteString("## Removing it\n\nRun `bash rollback.sh` in a cluster folder, or `sudo bash rollback.sh` in the server folder. In a cluster the operator is left in place.\n")
	return b.String()
}

// Project is the hand-over for a project: the process to follow, the README
// that describes it, and the package holding the README and every file.
type Project struct {
	Process []ProcessStep
	Readme  string
	Archive []byte
	Name    string
}

// BuildProject makes the package from the inputs alone.
func BuildProject(p Profile, snap *Snapshot, in Inputs, now time.Time) (*Project, error) {
	j, err := Create(p, snap, in, now)
	if err != nil {
		return nil, err
	}
	in = j.In
	root := "otelgenie-" + slug(in.Project) + "-" + slug(in.Env)
	others := j.choosable(kindK8s)

	folders := map[string]string{}
	files := map[string]string{}
	put := func(folder string, t *Target) {
		for name, body := range j.generate(t) {
			files[path.Join(folder, name)] = body
		}
	}
	for _, platform := range j.platforms() {
		components := []string{platform}
		for _, ci := range others {
			if !ci.Platform {
				components = append(components, ci.Name)
			}
		}
		sort.Strings(components)
		folders[platform] = "cluster-" + platform
		put(folders[platform], j.draftTarget(slug(in.Env)+"-"+platform, kindK8s, components))
	}
	// With a cluster in the project, a server is only assumed when something
	// chosen cannot run in a cluster.
	servers, beside := j.serverComponents(), j.serversBesideCluster()
	switch {
	case beside && in.Ingress == "":
		// The servers' files name the Ingress address, which is only known
		// once the Ingress exists. The process asks for it at that point.
	case beside || len(folders) == 0:
		folders[kindVM] = "server"
		put("server", j.draftTarget("server", kindVM, servers))
	}
	for _, e := range j.guides() {
		files["guides/"+e.Component+".md"] = j.guideText(e)
	}
	// One script for the checks, with what it needs to know about this project.
	if body, ok := snap.Files[projectScript]; ok {
		files["otelgenie.sh"] = body
		var clusters []string
		for _, platform := range j.platforms() {
			clusters = append(clusters, folders[platform])
		}
		yes := map[bool]string{true: "yes", false: "no"}
		operatorURL := ""
		if a := j.operatorArtifact(); a != nil {
			operatorURL = a.URL
		}
		var stacks []string
		if contains(in.Components, "app") {
			stacks = in.Stacks
		}
		var env strings.Builder
		for _, kv := range [][2]string{
			{"PROJECT", in.Project}, {"ENVIRONMENT", in.Env},
			{"DESTINATION", strings.TrimRight(in.Endpoint, "/")}, {"INGRESS", in.Ingress}, {"SEND_TO", strings.TrimRight(j.sendsTo(kindVM), "/")},
			{"NAMESPACE", k8sNamespace}, {"CURL_IMAGE", p.curlImage()},
			{"CLUSTER_FOLDERS", strings.Join(clusters, " ")}, {"SERVER_FOLDER", folders[kindVM]},
			{"SERVICE_USER", j.Man.Settings.ServiceUser},
			{"OPERATOR_URL", operatorURL},
			{"COLLECTOR_VERSION", j.Man.CollectorVersion}, {"PACKAGE_METHOD", p.PackageMethod}, {"PACKAGE_REPO_URL", p.PackageRepoURL},
			{"EXPECT_INGRESS", yes[j.serversBesideCluster()]},
			{"EXPECT_INSTRUMENTATION", yes[len(clusters) > 0 && contains(in.Components, "app")]},
			{"APPLICATION_STACKS", strings.Join(stacks, " ")},
		} {
			fmt.Fprintf(&env, "%s='%s'\n", kv[0], strings.ReplaceAll(kv[1], "'", `'\''`))
		}
		files["project.env"] = env.String()
		var pre strings.Builder
		for _, a := range j.Man.Artifacts {
			if a.Kind == "addon" && a.Name != "" && a.Check != "" {
				// name, detection command, file, where it comes from, kinds of
				// cluster that do not need it, kinds that come with it. "-" stands for an empty field.
				dash := func(v string) string {
					if v == "" {
						return "-"
					}
					return v
				}
				fmt.Fprintf(&pre, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Name, a.Check, dash(a.File), dash(a.URL), dash(strings.Join(a.SkipOn, ",")), dash(strings.Join(a.BuiltinOn, ",")))
			}
		}
		files["prerequisites.txt"] = pre.String()
		// The collector packages a server may need: family, architecture,
		// file and where the file comes from.
		var pkgs strings.Builder
		for _, a := range j.Man.Artifacts {
			if a.Kind == "package" {
				fmt.Fprintf(&pkgs, "%s\t%s\t%s\t%s\n", a.OS, a.Arch, a.File, a.URL)
			}
		}
		files["packages.txt"] = pkgs.String()
	}
	// A values file per service whose chart can carry the annotation; only
	// clusters use it.
	if len(j.platforms()) > 0 && contains(in.Components, "app") {
		for _, s := range in.Services {
			if len(s.chartKeys()) > 0 {
				files[serviceFolder(s)+"/values-otel.yaml"] = s.chartValues()
			}
		}
	}
	out := &Project{Process: j.processSteps(folders), Name: root + ".tar.gz"}
	out.Readme = j.readme(out.Process, folders)
	files["README.md"] = out.Readme

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range sortedKeys(files) {
		mode := int64(0o644)
		switch {
		case strings.HasSuffix(name, ".sh"):
			mode = 0o755
		case path.Base(name) == "otelcol.env":
			mode = 0o600
		}
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + name, Mode: mode, Size: int64(len(files[name])), ModTime: now}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(files[name])); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	out.Archive = buf.Bytes()
	return out, nil
}

func serviceFolder(s Service) string { return "services/" + slug(s.Name) }

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(notSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "project"
	}
	return s
}
