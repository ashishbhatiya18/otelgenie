package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (j *Journey) submit(now time.Time, t *Target, data map[string]any) {
	switch t.Step {
	case StepDiscover:
		j.submitDiscover(now, t, str(data, "paste"))
	case StepConfirm:
		j.submitConfirm(now, t, data)
	case StepReview:
		j.submitReview(now, t, str(data, "choice"))
	case StepApply:
		j.submitApply(now, t, data)
	case StepVerify:
		j.submitVerify(now, t, str(data, "paste"))
	}
}

func (j *Journey) submitDiscover(now time.Time, t *Target, paste string) {
	b, err := ParseBlock(paste, "DISCOVER")
	if err != nil {
		j.reject(now, t, err.Error())
		return
	}
	if b.Get("items") == "missing" {
		j.reject(now, t, "discover.items was not next to discover.sh on the server. Copy both files and run it again.")
		return
	}
	if t.Kind == kindK8s && b.Get("cluster") != "reachable" {
		j.reject(now, t, "kubectl could not reach a cluster from that machine. Log in to the cluster, check `kubectl config current-context`, and run discover.sh again.")
		return
	}
	j.emit(now, "d.facts", t.Name, map[string]any{"facts": b.Values})
	if t.Kind == kindK8s {
		j.noteClusterFacts(now, t, b)
	} else {
		if b.Get("systemd") != "yes" {
			j.flag(now, t, "This server does not run systemd, so the service cannot be installed by this guide.")
		}
		if b.Get("collector") == "none" {
			j.decide(now, t, "The collector package is not installed yet; the apply step will explain how to install it.")
		}
		for _, k := range b.WithPrefix("tcp.") {
			if b.Get(k) != "ok" {
				j.flag(now, t, "The collector endpoint %s cannot be reached from this server.", strings.TrimPrefix(k, "tcp."))
			}
		}
	}
	for _, ci := range j.choosable(t.Kind) {
		if ev := j.evidence(t, ci.Name); len(ev) > 0 {
			j.decide(now, t, "%s found: %s.", ci.Name, strings.Join(ev, ", "))
		} else {
			j.decide(now, t, "%s not found on this %s.", ci.Name, place(t))
		}
	}
	j.goTo(now, t, StepConfirm)
}

// choosable are the components the user picks per server: everything they
// asked for that is not switched on for every server anyway.
func (j *Journey) choosable(kind string) []ComponentInfo {
	var out []ComponentInfo
	for _, ci := range j.Cat.Components(kind) {
		if !ci.Always && contains(j.In.Components, ci.Name) {
			out = append(out, ci)
		}
	}
	return out
}

func (j *Journey) submitConfirm(now time.Time, t *Target, data map[string]any) {
	picked, _ := data["components"].(map[string]any)
	formRaw, _ := data["vars"].(map[string]any)
	form := map[string]string{}
	for k, v := range formRaw {
		if s, ok := v.(string); ok {
			form[k] = s
		}
	}
	var components []string
	var platforms []string
	for _, ci := range j.choosable(t.Kind) {
		if on, _ := picked[ci.Name].(bool); on {
			components = append(components, ci.Name)
			if ci.Platform {
				platforms = append(platforms, ci.Name)
			}
		}
	}
	sort.Strings(components)
	if t.Kind == kindK8s && len(platforms) != 1 {
		j.reject(now, t, "Tick exactly one kind of cluster. It decides how the collectors are set up; everything else is added to them.")
		return
	}
	vars, typed, missing := j.resolveVars(t, components, form)
	if len(missing) > 0 {
		j.reject(now, t, "These still need a value: "+strings.Join(missing, ", ")+".")
		return
	}
	for _, ci := range j.choosable(t.Kind) {
		if !contains(components, ci.Name) {
			j.decide(now, t, "%s will not be monitored on this %s.", ci.Name, place(t))
		}
	}
	j.emit(now, "d.selection", t.Name, map[string]any{"components": components, "vars": vars, "typed": typed})
	j.emitFiles(now, t, j.generate(t))
	j.goTo(now, t, StepReview)
}

func (j *Journey) submitReview(now time.Time, t *Target, choice string) {
	needs := j.openNeeds(t)
	switch choice {
	case "proceed", "have":
		if t.WaitingSince != "" {
			j.emit(now, "d.waiting_cleared", t.Name, map[string]any{})
		}
		j.goTo(now, t, StepApply)
	case "waiting":
		if len(needs) == 0 {
			j.reject(now, t, "Nothing is outstanding for this server.")
			return
		}
		var items []string
		for _, n := range needs {
			items = append(items, n.Component+": "+n.Name)
		}
		j.emit(now, "d.waiting", t.Name, map[string]any{"items": items})
	case "drop":
		if len(needs) == 0 {
			j.reject(now, t, "Nothing is outstanding for this server.")
			return
		}
		dropped := map[string]bool{}
		for _, n := range needs {
			dropped[n.Component] = true
		}
		var keep []string
		for _, c := range t.Selected {
			if !dropped[c] {
				keep = append(keep, c)
			}
		}
		deferred := append(append([]string{}, t.Deferred...), sortedKeys(dropped)...)
		sort.Strings(deferred)
		form := map[string]string{}
		for _, name := range t.Typed {
			form[name] = t.Vars[name]
		}
		vars, typed, _ := j.resolveVars(t, keep, form)
		j.decide(now, t, "Continuing without %s for now; add it later by going back to the confirm step.", strings.Join(sortedKeys(dropped), ", "))
		j.emit(now, "d.deferred", t.Name, map[string]any{"components": deferred})
		j.emit(now, "d.selection", t.Name, map[string]any{"components": keep, "vars": vars, "typed": typed})
		if t.WaitingSince != "" {
			j.emit(now, "d.waiting_cleared", t.Name, map[string]any{})
		}
		j.emitFiles(now, t, j.generate(t))
		j.goTo(now, t, StepApply)
	default:
		j.reject(now, t, "Choose one of the options.")
	}
}

func (j *Journey) stale(t *Target, now time.Time) (int, bool) {
	at, err := time.Parse(time.RFC3339, t.FactsAt)
	if err != nil || t.StaleAck {
		return 0, false
	}
	days := int(now.Sub(at).Hours() / 24)
	return days, days >= j.Man.Settings.StaleDiscoveryDays
}

func (j *Journey) submitApply(now time.Time, t *Target, data map[string]any) {
	if _, isStale := j.stale(t, now); isStale {
		switch str(data, "choice") {
		case "rediscover":
			j.decide(now, t, "Discovery was out of date; running it again.")
			j.goTo(now, t, StepDiscover)
		case "continue":
			j.flag(now, t, "Continued with out-of-date discovery results.")
			j.emit(now, "d.stale_ack", t.Name, map[string]any{})
		default:
			j.reject(now, t, "Choose whether to run discovery again.")
		}
		return
	}
	b, err := ParseBlock(str(data, "paste"), "APPLY")
	if err != nil {
		j.reject(now, t, err.Error())
		return
	}
	res := ApplyResult{Result: b.Get("result")}
	for _, prefix := range []string{"placeholder.", "validate.", "err."} {
		for _, k := range b.WithPrefix(prefix) {
			line := b.Get(k)
			res.Detail = append(res.Detail, line)
			if prefix != "placeholder." {
				if d := Diagnose(line); d != nil && !hasDiagnosis(res.Diagnoses, *d) {
					res.Diagnoses = append(res.Diagnoses, *d)
				}
			}
		}
	}
	j.emit(now, "d.apply", t.Name, res)
	if res.Result == "ok" {
		j.goTo(now, t, StepVerify)
	}
}

func hasDiagnosis(list []Diagnosis, d Diagnosis) bool {
	for _, x := range list {
		if x == d {
			return true
		}
	}
	return false
}

func (j *Journey) submitVerify(now time.Time, t *Target, paste string) {
	b, err := ParseBlock(paste, "VERIFY")
	if err != nil {
		j.reject(now, t, err.Error())
		return
	}
	res := j.evaluateVerify(t, b)
	j.emit(now, "d.verify", t.Name, res)
	if res.Pass {
		j.goTo(now, t, StepDone)
	}
}

// arrived names the collector counters that show a signal coming in. They are
// per signal because one receiver can serve several, as OTLP does.
var arrived = map[string][]string{
	"metrics": {"receiver_accepted_metric_points", "scraper_scraped"},
	"logs":    {"receiver_accepted_log_records"},
	"events":  {"receiver_accepted_log_records"},
	"traces":  {"receiver_accepted_spans"},
}

// evaluateVerify reads the collector's own counters and recent log lines and
// decides, per component, whether data is really flowing.
func (j *Journey) evaluateVerify(t *Target, b *Block) VerifyResult {
	res := VerifyResult{}
	problem := func(format string, a ...any) { res.Problems = append(res.Problems, fmt.Sprintf(format, a...)) }

	confDir := "conf.d/"
	if t.Kind == kindK8s {
		confDir = "manifests/"
	}
	if s := b.Get("service"); s != "active" && t.Kind == kindK8s {
		problem("Not every collector pod is running (%s).", s)
	} else if s != "active" {
		problem("The service is not running (state: %s).", s)
	}
	if p := b.Get("placeholders"); p != "0" && p != "unknown" && p != "" {
		problem("%s secret(s) are still not filled in on the %s. Run apply.sh again and enter them.", p, place(t))
	}
	if b.Get("metrics") != "ok" {
		problem("The collector's own status page did not answer, so data flow could not be checked.")
	}
	if r, err := strconv.Atoi(b.Get("restarts")); err == nil && r > 2 && t.Kind == kindK8s {
		problem("A collector pod has restarted %d times, which means it keeps crashing.", r)
	} else if err == nil && r > 2 {
		problem("The service has restarted %d times, which means it keeps crashing.", r)
	}

	// The files on the server must be the ones this journey generated.
	deployed := map[string]string{}
	for _, k := range b.WithPrefix("file.") {
		deployed[strings.TrimPrefix(k, "file.")] = b.Get(k)
	}
	for path, sha := range t.Files {
		if !strings.HasPrefix(path, confDir) {
			continue
		}
		switch got, ok := deployed[path]; {
		case !ok:
			problem("%s is missing on the %s. Copy the files again and re-run apply.sh.", path, place(t))
		case got != sha:
			problem("%s on the %s is not the file that was generated. It was edited by hand or is an older copy.", path, place(t))
		}
		delete(deployed, path)
	}
	for _, path := range sortedKeys(deployed) {
		problem("%s is on the %s but was not generated by this journey.", path, place(t))
	}

	// Collector log lines, grouped by the receiver or exporter they name.
	errs := map[string][]string{}
	exporters := map[string]bool{}
	for _, k := range b.WithPrefix("err.") {
		line := b.Get(k)
		id, kind := errorComponent(line)
		errs[id] = append(errs[id], line)
		if kind == "exporter" {
			exporters[id] = true
		}
	}
	count := func(who string, names ...string) int {
		total := 0
		for _, k := range b.WithPrefix("m.") {
			metric, label, _ := strings.Cut(strings.TrimPrefix(k, "m."), "|")
			if label != who {
				continue
			}
			for _, n := range names {
				if strings.HasPrefix(metric, "otelcol_"+n) {
					v, _ := strconv.Atoi(b.Get(k))
					total += v
				}
			}
		}
		return total
	}

	failing := false
	listed := map[string]bool{}
	for _, e := range j.selectedEntries(t) {
		for _, r := range e.Receivers {
			// Two files of a component may read the same signal from the
			// same receiver; that is still one thing to report.
			key := e.Component + " " + e.Signal + " " + r
			if listed[key] {
				continue
			}
			listed[key] = true
			row := VerifyRow{Component: e.Component, Signal: e.Signal, Name: r}
			row.Count = count(r, arrived[e.Signal]...)
			switch {
			case len(errs[r]) > 0:
				// A scrape that fails still reports a few points, so the log
				// line is the better witness.
				row.Status = "failing"
				row.Note = errs[r][len(errs[r])-1]
				row.Diagnosis = Diagnose(row.Note)
				failing = true
			case row.Count > 0:
				row.Status = "receiving"
			case e.Signal == "metrics" && !e.pushed():
				row.Status = "silent"
				row.Note = "Nothing collected yet. Wait a minute and run verify.sh again."
				failing = true
			default:
				row.Status = "silent"
				row.Note = "Nothing yet. That is normal until something new is logged or the application sends data."
			}
			res.Rows = append(res.Rows, row)
		}
	}

	for _, k := range b.WithPrefix("m.otelcol_exporter_") {
		_, label, _ := strings.Cut(k, "|")
		exporters[label] = true
	}
	if len(exporters) == 0 {
		exporters["otlphttp"] = true
	}
	delivering := false
	for _, x := range sortedKeys(exporters) {
		row := VerifyRow{Component: "base", Signal: "delivery", Name: x}
		row.Count = count(x, "exporter_sent")
		failed := count(x, "exporter_send_failed", "exporter_enqueue_failed")
		switch {
		case len(errs[x]) > 0 || failed > 0:
			row.Status = "not-delivering"
			if len(errs[x]) > 0 {
				row.Note = errs[x][len(errs[x])-1]
				row.Diagnosis = Diagnose(row.Note)
			} else {
				row.Note = fmt.Sprintf("%d item(s) could not be delivered.", failed)
			}
		case row.Count > 0:
			row.Status = "delivering"
			delivering = true
		default:
			row.Status = "not-delivering"
			row.Note = "Nothing has been delivered yet. Wait a minute and run verify.sh again."
		}
		res.Rows = append(res.Rows, row)
	}
	if !delivering {
		problem("Data is not reaching the collector endpoint.")
	}
	if failing {
		problem("At least one component is not producing data.")
	}
	for _, line := range errs[""] {
		if d := Diagnose(line); d != nil {
			problem("%s %s", d.Cause, d.Fix)
		}
	}
	res.Pass = len(res.Problems) == 0
	return res
}
