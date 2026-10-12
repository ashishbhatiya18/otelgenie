package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ViewBlock is one piece of a screen. Kinds: text, command, list, ok, warn,
// error, table.
type ViewBlock struct {
	Kind  string     `json:"kind"`
	Title string     `json:"title,omitempty"`
	Text  string     `json:"text,omitempty"`
	Items []string   `json:"items,omitempty"`
	Rows  [][]string `json:"rows,omitempty"`
}

type Field struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Hint     string `json:"hint,omitempty"`
	Required bool   `json:"required"`
}

type Item struct {
	Component string   `json:"component"`
	Titles    []string `json:"titles"`
	Found     bool     `json:"found"`
	Evidence  string   `json:"evidence"`
	Fields    []Field  `json:"fields,omitempty"`
}

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Form says what the screen expects back. Kinds: paste, confirm, choice, none.
type Form struct {
	Kind    string   `json:"kind"`
	Label   string   `json:"label,omitempty"`
	Items   []Item   `json:"items,omitempty"`
	Options []Option `json:"options,omitempty"`
}

// View is one screen for one server. Its Text() is recorded in the log, so a
// reviewer sees exactly what the user saw.
type View struct {
	Target   string      `json:"target"`
	Step     string      `json:"step"`
	Number   int         `json:"number"`
	Of       int         `json:"of"`
	Title    string      `json:"title"`
	Why      string      `json:"why"`
	Blocks   []ViewBlock `json:"blocks"`
	Form     Form        `json:"form"`
	Download bool        `json:"download"`
}

func (v *View) add(kind, title, text string, items ...string) {
	v.Blocks = append(v.Blocks, ViewBlock{Kind: kind, Title: title, Text: text, Items: items})
}

func (v View) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] Step %d of %d: %s\n%s\n", v.Target, v.Number, v.Of, v.Title, v.Why)
	for _, bl := range v.Blocks {
		b.WriteString("\n")
		if bl.Title != "" {
			b.WriteString(bl.Title + "\n")
		}
		switch bl.Kind {
		case "command":
			b.WriteString("    " + strings.ReplaceAll(bl.Text, "\n", "\n    ") + "\n")
		case "ok", "warn", "error":
			b.WriteString(strings.ToUpper(bl.Kind) + ": " + bl.Text + "\n")
		default:
			if bl.Text != "" {
				b.WriteString(bl.Text + "\n")
			}
		}
		for _, it := range bl.Items {
			b.WriteString("  - " + it + "\n")
		}
		for _, row := range bl.Rows {
			b.WriteString("  " + strings.Join(row, " | ") + "\n")
		}
	}
	switch v.Form.Kind {
	case "paste":
		b.WriteString("\n(waiting for: " + v.Form.Label + ")\n")
	case "choice":
		for _, o := range v.Form.Options {
			b.WriteString("\n( ) " + o.Label)
		}
		b.WriteString("\n")
	case "confirm":
		for _, it := range v.Form.Items {
			mark := "[ ]"
			if it.Found {
				mark = "[x]"
			}
			fmt.Fprintf(&b, "\n%s %s: %s", mark, it.Component, it.Evidence)
			for _, f := range it.Fields {
				fmt.Fprintf(&b, "\n      %s = %s", f.Label, f.Value)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func ago(from string, now time.Time) string {
	at, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < 2*time.Hour:
		return "just now"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func (j *Journey) view(t *Target, now time.Time) View {
	v := View{Target: t.Name, Step: t.Step, Number: stepIndex(t.Step) + 1, Of: len(stepOrder), Download: len(t.Files) > 0}
	if t.LastReject != "" {
		v.add("error", "", t.LastReject)
	}
	if t.Kind == kindK8s {
		j.viewK8s(&v, t, now)
		return v
	}
	folder := j.archiveName(t)
	switch t.Step {
	case StepDiscover:
		v.Title = "Look at the server"
		v.Why = "Before anything is set up, a small script reads a few facts from the server: its size, whether the collector is installed, and which of your components are running there. It only reads; it changes nothing and prints no passwords."
		v.add("text", "1. Get the two files onto the server", "Download the files for this server below, then copy the folder across. From your Mac's Terminal:")
		v.add("command", "On your Mac", fmt.Sprintf("cd ~/Downloads && tar xzf %s.tar.gz && scp -r %s YOUR-USER@%s:", folder, folder, t.Name))
		v.add("text", "2. Run the script on the server", "")
		v.add("command", "On the server", fmt.Sprintf("bash %s/discover.sh", folder))
		v.add("text", "3. Paste what it printed", "Copy everything from the line OTELGENIE-DISCOVER down to the line starting END OTELGENIE-DISCOVER.")
		v.Form = Form{Kind: "paste", Label: "Output of discover.sh"}

	case StepConfirm:
		v.Title = "Confirm what to monitor on this server"
		v.Why = "This is what the script found. Tick what should be monitored here. Things that are always collected (the server's own health) are not listed."
		if t.Facts["systemd"] != "yes" {
			v.add("warn", "", "This server does not run systemd. This guide installs the collector as a systemd service, so it cannot finish on this server.")
		}
		for _, k := range sortedKeys(t.Facts) {
			if strings.HasPrefix(k, "tcp.") && t.Facts[k] != "ok" {
				v.add("warn", "", "The server could not connect to the collector endpoint "+strings.TrimPrefix(k, "tcp.")+". You can continue, but no data will arrive until the network team allows this connection.")
			}
		}
		v.add("list", "About this server",
			"", "Name it reports: "+t.Facts["host"],
			"Operating system: "+t.Facts["os_id"]+" "+t.Facts["os_version"]+" ("+t.Facts["arch"]+")",
			"Collector installed: "+t.Facts["collector"])
		v.Form = Form{Kind: "confirm", Items: j.confirmItems(t)}
		if len(v.Form.Items) == 0 {
			v.add("text", "", "None of the components you chose need a decision for this server. Continue to generate the files.")
		}

	case StepReview:
		v.Title = "Review what will be installed"
		v.Why = "The files for this server are ready. Nothing has been changed on the server yet."
		var what []string
		for _, e := range j.selectedEntries(t) {
			if e.Signal != "base" {
				what = append(what, e.Title)
			}
		}
		v.add("list", "What will be collected", "", what...)
		v.add("list", "How it will run", "",
			"As the systemd service otelgenie-otelcol",
			fmt.Sprintf("Limited to %d MB of memory and %d%% of one CPU", j.memoryMaxMB(t), j.Man.Settings.CPUQuotaPercent),
			"Sending to "+j.sendsTo(t.Kind)+" for "+j.In.Project+" / "+j.In.Env)
		v.add("list", "Secrets you will type on the server, never here", "", j.secretVars(t)...)
		needs := j.openNeeds(t)
		if len(needs) > 0 {
			var items []string
			for _, n := range needs {
				items = append(items, fmt.Sprintf("%s: %s. A ready-to-send request is in requests/%s-%s.md", n.Component, n.Note, n.Component, n.Name))
			}
			v.add("warn", "Needed from someone else", "These depend on another person. Send them the request file from the download.", items...)
		}
		if len(t.Deferred) > 0 {
			v.add("text", "", "Set aside for later: "+strings.Join(t.Deferred, ", ")+".")
		}
		switch {
		case t.WaitingSince != "":
			v.add("warn", "", "Waiting since "+ago(t.WaitingSince, now)+" for: "+strings.Join(t.WaitingItems, "; ")+".")
			v.Form = Form{Kind: "choice", Options: []Option{
				{"have", "I have what was needed. Continue."},
				{"drop", "Still waiting. Continue without those components for now."},
			}}
		case len(needs) > 0:
			v.Form = Form{Kind: "choice", Options: []Option{
				{"proceed", "These are already in place. Continue."},
				{"waiting", "I have asked and am waiting. Keep my place."},
				{"drop", "Continue without those components for now."},
			}}
		default:
			v.Form = Form{Kind: "choice", Options: []Option{{"proceed", "Looks right. Continue."}}}
		}

	case StepApply:
		v.Title = "Install on the server"
		v.Why = "This copies the configuration into place and starts the collector as a service. It needs sudo on the server. You can run it again safely."
		if days, isStale := j.stale(t, now); isStale {
			v.add("warn", "", fmt.Sprintf("The server was last looked at %d days ago. Things may have changed since.", days))
			v.Form = Form{Kind: "choice", Options: []Option{
				{"rediscover", "Look at the server again first (recommended)."},
				{"continue", "Continue with what was found then."},
			}}
			break
		}
		if t.Apply != nil && t.Apply.Result != "ok" {
			j.explainApply(&v, t)
		}
		if t.Facts["collector"] == "none" {
			v.add("list", "First: install the collector package", "", j.packageHint(t)...)
		}
		v.add("text", "1. Copy the new files to the server", "Download the files for this server below (they have changed since the first step), then:")
		v.add("command", "On your Mac", fmt.Sprintf("cd ~/Downloads && tar xzf %s.tar.gz && scp -r %s YOUR-USER@%s:", folder, folder, t.Name))
		v.add("text", "2. Run the installer", "It will ask you to type each secret. Typing is hidden.")
		v.add("command", "On the server", fmt.Sprintf("cd %s && sudo bash apply.sh", folder))
		v.add("text", "3. Paste what it printed", "From the line OTELGENIE-APPLY down to the line starting END OTELGENIE-APPLY.")
		v.Form = Form{Kind: "paste", Label: "Output of apply.sh"}

	case StepVerify:
		v.Title = "Check that data is flowing"
		v.Why = "The service is running. This step checks that each component is producing data and that it is reaching the collector endpoint. Wait about a minute after installing before running it."
		if t.Verify != nil && !t.Verify.Pass {
			j.explainVerify(&v, t)
		}
		v.add("command", "On the server", fmt.Sprintf("cd %s && sudo bash verify.sh", folder))
		v.add("text", "", "Paste everything from the line OTELGENIE-VERIFY down to the line starting END OTELGENIE-VERIFY.")
		v.Form = Form{Kind: "paste", Label: "Output of verify.sh"}

	case StepDone:
		v.Title = "Telemetry is flowing from this server"
		v.Why = "Everything selected for this server is producing data and it is reaching the collector endpoint."
		if t.Verify != nil {
			v.Blocks = append(v.Blocks, verifyTable(t.Verify))
		}
		for _, e := range j.selectedEntries(t) {
			for _, n := range e.Needs {
				if n.Kind == "handout" {
					v.add("warn", "One thing left: "+e.Title, n.Attrs["note"])
				}
			}
		}
		if len(t.Deferred) > 0 {
			v.add("text", "", "Still to do later on this server: "+strings.Join(t.Deferred, ", ")+". Go back to the confirm step when their owners have replied.")
		}
		v.Form = Form{Kind: "none"}
	}
	return v
}

func (j *Journey) confirmItems(t *Target) []Item {
	var items []Item
	for _, ci := range j.choosable(t.Kind) {
		ev := j.evidence(t, ci.Name)
		it := Item{Component: ci.Name, Titles: ci.Titles, Found: len(ev) > 0}
		detectable := false
		for _, e := range j.candidateEntries(t.Kind) {
			for _, p := range e.Probes {
				if e.Component == ci.Name && (p.Kind == "port" || p.Kind == "unit" || p.Kind == "glob" || p.Kind == "distro") {
					detectable = true
				}
			}
		}
		switch {
		case it.Found:
			it.Evidence = "Found: " + strings.Join(ev, ", ") + "."
		case !detectable:
			// Nothing on the server can confirm this one, so go by what the
			// user asked for.
			it.Found = true
			it.Evidence = "Included because you chose it. There is nothing on the " + place(t) + " to check for this one."
		default:
			it.Evidence = "Not found on this " + place(t) + ". Tick it only if it runs somewhere this " + place(t) + " can reach."
		}
		seen := map[string]bool{}
		for _, e := range j.candidateEntries(t.Kind) {
			if e.Component != ci.Name {
				continue
			}
			for _, p := range e.Probes {
				if p.Var == "" || p.Kind == "input" || p.Kind == "pod" || seen[p.Var] {
					continue
				}
				seen[p.Var] = true
				f := Field{Name: p.Var, Label: p.Help, Required: p.Kind == "ask"}
				if f.Label == "" {
					f.Label = "Location (" + p.Arg + ")"
				}
				switch {
				case p.Kind == "port" && t.Facts["port."+p.Arg] == "open":
					f.Value = strings.ReplaceAll(p.As, "{port}", p.Arg)
				case p.Kind == "glob" && t.Facts["glob."+p.Arg] != "" && t.Facts["glob."+p.Arg] != "0" && t.Facts["glob."+p.Arg] != "denied":
					f.Value = p.Arg
				case p.Kind == "svc" && svcAddress(p, t.Facts["svc."+p.Arg]) != "":
					f.Value = svcAddress(p, t.Facts["svc."+p.Arg])
				case p.Kind != "ask":
					f.Hint = "Not found. Leave empty to use the usual location, or type where it is."
				}
				// Two probes may fill the same variable; show the one that hit.
				it.Fields = append(it.Fields, f)
			}
		}
		// A later probe for the same variable may have found what an earlier one missed.
		for i := range it.Fields {
			if it.Fields[i].Value != "" {
				continue
			}
			for _, e := range j.candidateEntries(t.Kind) {
				for _, p := range e.Probes {
					if p.Var == it.Fields[i].Name && p.Kind == "glob" {
						if c := t.Facts["glob."+p.Arg]; c != "" && c != "0" && c != "denied" {
							it.Fields[i].Value, it.Fields[i].Hint = p.Arg, ""
						}
					}
				}
			}
		}
		items = append(items, it)
	}
	return items
}

func (j *Journey) explainApply(v *View, t *Target) {
	a := t.Apply
	switch a.Result {
	case "need-root":
		v.add("error", "The installer was run without sudo", "Run it again with sudo in front, exactly as shown below.")
	case "corrupt-files":
		v.add("error", "The files on the server are incomplete or were changed", "Download the files again, copy the whole folder across, and run the installer again.")
	case "missing-collector":
		v.add("error", "The collector package is not installed on the server", "")
		v.add("list", "Install it, then run the installer again", "", j.packageHint(t)...)
	case "placeholders":
		v.add("error", "Some secrets were not entered", "The installer stopped before starting the service because these are still empty. Run it again and type them when asked.", a.Detail...)
	case "invalid-config":
		v.add("error", "The collector rejected the configuration", "", a.Detail...)
	case "service-failed":
		v.add("error", "The service was installed but did not stay running", "", a.Detail...)
	default:
		v.add("error", "The installer did not finish", "Result reported: "+a.Result)
	}
	for _, d := range a.Diagnoses {
		v.add("warn", d.Cause, d.Fix)
	}
}

func verifyTable(r *VerifyResult) ViewBlock {
	words := map[string]string{
		"receiving": "Receiving data", "silent": "Nothing yet", "failing": "Failing",
		"delivering": "Reaching the endpoint", "not-delivering": "Not reaching the endpoint",
	}
	bl := ViewBlock{Kind: "table", Title: "Status", Rows: [][]string{{"Component", "Kind", "Status", "Count"}}}
	rows := append([]VerifyRow{}, r.Rows...)
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].Component < rows[b].Component })
	for _, row := range rows {
		comp := row.Component
		if comp == "base" {
			comp = "delivery (" + row.Name + ")"
		}
		bl.Rows = append(bl.Rows, []string{comp, row.Signal, words[row.Status], fmt.Sprint(row.Count)})
	}
	return bl
}

func (j *Journey) explainVerify(v *View, t *Target) {
	v.add("error", "Not working yet", "", t.Verify.Problems...)
	v.Blocks = append(v.Blocks, verifyTable(t.Verify))
	seen := map[Diagnosis]bool{}
	for _, row := range t.Verify.Rows {
		if row.Diagnosis != nil && !seen[*row.Diagnosis] {
			seen[*row.Diagnosis] = true
			v.add("warn", row.Component+": "+row.Diagnosis.Cause, row.Diagnosis.Fix)
		}
	}
	v.add("text", "", "Fix what is listed, wait a minute, then run the check again.")
}
