package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Finding struct {
	Severity string `json:"severity"` // problem | warning | info
	Seq      int    `json:"seq"`
	Target   string `json:"target,omitempty"`
	Step     string `json:"step,omitempty"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

type FileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"` // added | changed | removed
	SHA    string `json:"sha,omitempty"`
}

// StepDetail is one thing the user did and everything that followed from it.
type StepDetail struct {
	Seq       int          `json:"seq"`
	TS        string       `json:"ts"`
	Gap       string       `json:"gap,omitempty"`
	Target    string       `json:"target,omitempty"`
	Step      string       `json:"step,omitempty"`
	Action    string       `json:"action"`
	Input     string       `json:"input,omitempty"`
	Rejected  string       `json:"rejected,omitempty"`
	Decisions []string     `json:"decisions,omitempty"`
	Outcome   string       `json:"outcome,omitempty"`
	Files     []FileChange `json:"files,omitempty"`
	Saw       string       `json:"saw,omitempty"`
}

type TargetSummary struct {
	Name   string `json:"name"`
	Step   string `json:"step"`
	Done   bool   `json:"done"`
	Status string `json:"status"`
}

type RerunResult struct {
	Same  bool     `json:"same"`
	Diffs []string `json:"diffs,omitempty"`
	Error string   `json:"error,omitempty"`
}

// Report is the reviewer's view of someone else's journey.
type Report struct {
	Meta         BundleMeta      `json:"meta"`
	Template     string          `json:"template"`
	ChainOK      bool            `json:"chain_ok"`
	ChainProblem string          `json:"chain_problem,omitempty"`
	CodeMatches  bool            `json:"code_matches"`
	Inputs       Inputs          `json:"inputs"`
	Profile      Profile         `json:"profile"`
	ReviewerDiff []string        `json:"reviewer_diff,omitempty"`
	Verdict      string          `json:"verdict"`
	Targets      []TargetSummary `json:"targets"`
	Findings     []Finding       `json:"findings"`
	Steps        []StepDetail    `json:"steps"`
	Rerun        RerunResult     `json:"rerun"`
}

var stepTitles = map[string]string{
	StepDiscover: "Look at the server", StepConfirm: "Confirm what to monitor", StepReview: "Review what will be installed",
	StepApply: "Install on the server", StepVerify: "Check that data is flowing", StepDone: "Finished",
}

func describeInput(e Event, p inputPayload) (action, input string) {
	switch e.Type {
	case "in.create":
		var d struct {
			Inputs Inputs `json:"inputs"`
		}
		json.Unmarshal(e.Data, &d)
		return "Started the journey", fmt.Sprintf("project=%s\nenvironment=%s\nendpoint=%s\nsignals=%s\ncomponents=%s",
			d.Inputs.Project, d.Inputs.Env, d.Inputs.Endpoint, strings.Join(d.Inputs.Signals, ","), strings.Join(d.Inputs.Components, ","))
	case "in.add_target":
		return "Added a server", str(p.Data, "name")
	case "in.note":
		return "Wrote a note", str(p.Data, "text")
	case "in.resume":
		return "Came back to the page", ""
	case "in.redo":
		return "Went back to an earlier step", p.Step
	}
	if paste := str(p.Data, "paste"); paste != "" {
		return "Pasted script output", paste
	}
	if choice := str(p.Data, "choice"); choice != "" {
		return "Chose an option", choice
	}
	if picked, ok := p.Data["components"].(map[string]any); ok {
		var lines []string
		for _, k := range sortedKeys(picked) {
			lines = append(lines, fmt.Sprintf("%s=%v", k, picked[k]))
		}
		if vars, ok := p.Data["vars"].(map[string]any); ok {
			for _, k := range sortedKeys(vars) {
				if s, _ := vars[k].(string); s != "" {
					lines = append(lines, fmt.Sprintf("%s=%s", k, s))
				}
			}
		}
		return "Confirmed components", strings.Join(lines, "\n")
	}
	return "Submitted an empty form", ""
}

func gapText(d time.Duration) string {
	switch {
	case d < 24*time.Hour:
		return ""
	case d < 48*time.Hour:
		return "1 day later"
	}
	return fmt.Sprintf("%d days later", int(d.Hours()/24))
}

// Analyze walks a bundle and says what happened and where it went wrong.
// reviewer is the reviewer's own setup fingerprint, if they have one.
func Analyze(b *Bundle, reviewer *Fingerprint, claimedCode string) *Report {
	r := &Report{Meta: b.Meta, Profile: b.Profile, Template: b.Snap.Repo + " at " + b.Snap.Commit}
	add := func(sev string, e Event, step, title, detail string) {
		r.Findings = append(r.Findings, Finding{Severity: sev, Seq: e.Seq, Target: e.Target, Step: step, Title: title, Detail: detail})
	}

	bad, reason := VerifyChain(b.Events)
	r.ChainOK, r.ChainProblem = bad == 0, reason
	if !r.ChainOK {
		r.Findings = append(r.Findings, Finding{Severity: "problem", Seq: bad, Title: "The recording is damaged or was edited", Detail: reason + ". Everything from that point on cannot be trusted."})
	}
	actual := SessionCode(b.Events)
	r.CodeMatches = claimedCode == "" || strings.EqualFold(strings.TrimSpace(claimedCode), actual)
	if !r.CodeMatches {
		r.Findings = append(r.Findings, Finding{Severity: "problem", Title: "This is not the session the user described",
			Detail: fmt.Sprintf("The user's session code is %s but this bundle ends at %s. The bundle is incomplete or is from a different point in time.", claimedCode, actual)})
	}
	if reviewer != nil {
		r.ReviewerDiff = DiffSegments(b.Meta.Fingerprint.Segments, reviewer.Segments)
		if len(r.ReviewerDiff) > 0 {
			r.Findings = append(r.Findings, Finding{Severity: "warning", Title: "Their one-time setup differs from yours",
				Detail: "Differing parts: " + strings.Join(r.ReviewerDiff, ", ") + ". Compare the setup values on the next page."})
		}
	}

	files := map[string]map[string]string{}
	steps := map[string]string{}
	rejects := map[string]int{}
	waiting := map[string]Event{}
	var cur *StepDetail
	var lastInput time.Time
	flush := func() {
		if cur != nil {
			r.Steps = append(r.Steps, *cur)
		}
	}
	for _, e := range b.Events {
		var p inputPayload
		json.Unmarshal(e.Data, &p)
		if e.IsInput() {
			flush()
			action, input := describeInput(e, p)
			step := p.Step
			if step == "" {
				step = steps[e.Target]
			}
			cur = &StepDetail{Seq: e.Seq, TS: e.TS, Target: e.Target, Step: step, Action: action, Input: input}
			if !lastInput.IsZero() {
				cur.Gap = gapText(e.Time().Sub(lastInput))
				if cur.Gap != "" && e.Type != "in.resume" {
					add("info", e, step, "Pause: "+cur.Gap, "")
				}
			}
			lastInput = e.Time()
			switch e.Type {
			case "in.note":
				add("info", e, step, "Note from the user", input)
			case "in.create":
				json.Unmarshal(e.Data, &struct {
					Inputs *Inputs `json:"inputs"`
				}{&r.Inputs})
			}
			continue
		}
		if cur == nil {
			continue
		}
		var d map[string]any
		json.Unmarshal(e.Data, &d)
		step := str(d, "step")
		switch e.Type {
		case "d.step":
			steps[e.Target] = step
			rejects[e.Target] = 0
			cur.Outcome = "Moved to: " + stepTitles[step]
		case "d.rejected":
			cur.Rejected = str(d, "reason")
			rejects[e.Target]++
			sev := "warning"
			if rejects[e.Target] >= 3 {
				sev = "problem"
			}
			add(sev, e, step, fmt.Sprintf("Input not accepted (attempt %d at this step)", rejects[e.Target]), cur.Rejected)
		case "d.decision":
			cur.Decisions = append(cur.Decisions, str(d, "text"))
			if flagged, _ := d["flag"].(bool); flagged {
				add("warning", e, step, str(d, "text"), "")
			}
		case "d.files":
			raw, _ := d["files"].(map[string]any)
			next := map[string]string{}
			for path, sha := range raw {
				next[path], _ = sha.(string)
			}
			prev := files[e.Target]
			for _, path := range sortedKeys(next) {
				switch old, ok := prev[path]; {
				case !ok:
					cur.Files = append(cur.Files, FileChange{path, "added", next[path]})
				case old != next[path]:
					cur.Files = append(cur.Files, FileChange{path, "changed", next[path]})
				}
			}
			for _, path := range sortedKeys(prev) {
				if _, ok := next[path]; !ok {
					cur.Files = append(cur.Files, FileChange{path, "removed", ""})
				}
			}
			files[e.Target] = next
		case "d.waiting":
			waiting[e.Target] = e
			items, _ := d["items"].([]any)
			var names []string
			for _, it := range items {
				names = append(names, fmt.Sprint(it))
			}
			add("info", e, steps[e.Target], "Waiting on someone else", strings.Join(names, "; "))
		case "d.waiting_cleared":
			if w, ok := waiting[e.Target]; ok {
				add("info", e, steps[e.Target], "Stopped waiting after "+strings.TrimSuffix(gapOrShort(e.Time().Sub(w.Time())), " later"), "")
				delete(waiting, e.Target)
			}
		case "d.deferred":
			comps, _ := d["components"].([]any)
			add("info", e, steps[e.Target], "Components set aside for later", strings.Trim(fmt.Sprint(comps), "[]"))
		case "d.apply":
			var a ApplyResult
			json.Unmarshal(e.Data, &a)
			cur.Outcome = "Installer result: " + a.Result
			if a.Result != "ok" {
				detail := strings.Join(a.Detail, "\n")
				for _, dg := range a.Diagnoses {
					detail += "\nLikely cause: " + dg.Cause + " " + dg.Fix
				}
				add("problem", e, StepApply, "Install failed: "+a.Result, strings.TrimSpace(detail))
			}
		case "d.verify":
			var v VerifyResult
			json.Unmarshal(e.Data, &v)
			if v.Pass {
				cur.Outcome = "Check passed"
				break
			}
			cur.Outcome = "Check failed"
			detail := strings.Join(v.Problems, "\n")
			for _, row := range v.Rows {
				if row.Status == "failing" || row.Status == "not-delivering" {
					detail += fmt.Sprintf("\n%s (%s): %s", row.Name, row.Status, row.Note)
					if row.Diagnosis != nil {
						detail += "\nLikely cause: " + row.Diagnosis.Cause
					}
				}
			}
			add("problem", e, StepVerify, "Data was not flowing", detail)
		case "d.view":
			cur.Saw = b.Blobs[str(d, "sha")]
		}
	}
	flush()
	for target, w := range waiting {
		r.Findings = append(r.Findings, Finding{Severity: "warning", Seq: w.Seq, Target: target, Step: steps[target], Title: "Still waiting on someone else when the bundle was exported"})
	}

	r.Rerun = Rerun(b)
	if r.Rerun.Error != "" {
		r.Findings = append(r.Findings, Finding{Severity: "problem", Title: "The session could not be re-run", Detail: r.Rerun.Error})
	} else if !r.Rerun.Same {
		r.Findings = append(r.Findings, Finding{Severity: "problem", Title: "The tool behaves differently now than when this was recorded",
			Detail: "Re-running the user's inputs gives different results. This points at the tool or a version difference, not at the user.\n" + strings.Join(r.Rerun.Diffs, "\n")})
	}

	// Final position of each server, which is usually the answer to "where
	// did it go wrong".
	var stuck []string
	for _, name := range sortedKeys(steps) {
		ts := TargetSummary{Name: name, Step: steps[name], Done: steps[name] == StepDone}
		ts.Status = "Finished"
		if !ts.Done {
			ts.Status = "Stopped at: " + stepTitles[ts.Step]
			why := ""
			for i := len(r.Findings) - 1; i >= 0; i-- {
				f := r.Findings[i]
				if f.Target == name && f.Severity == "problem" {
					why = f.Title
					if first, _, _ := strings.Cut(f.Detail, "\n"); first != "" {
						why += ": " + first
					}
					break
				}
			}
			if why == "" {
				why = "no error recorded; the user simply has not completed this step"
			}
			stuck = append(stuck, fmt.Sprintf("%s stopped at \"%s\" (%s)", name, stepTitles[ts.Step], why))
		}
		r.Targets = append(r.Targets, ts)
	}
	switch {
	case !r.ChainOK:
		r.Verdict = "The recording is damaged, so conclusions below are unreliable."
	case len(steps) == 0:
		r.Verdict = "The user started a journey but never added a server."
	case len(stuck) == 0:
		r.Verdict = "Every server reached the end. Nothing is recorded as failing."
	default:
		r.Verdict = strings.Join(stuck, ". ") + "."
	}
	rank := map[string]int{"problem": 0, "warning": 1, "info": 2}
	sort.SliceStable(r.Findings, func(a, c int) bool { return rank[r.Findings[a].Severity] < rank[r.Findings[c].Severity] })
	return r
}

func gapOrShort(d time.Duration) string {
	if g := gapText(d); g != "" {
		return g
	}
	return fmt.Sprintf("%d hours later", int(d.Hours()))
}

// Rerun feeds the recorded inputs through this build of the engine and
// compares every derived event with the recording. Identical means the tool
// did what it was built to do, so the cause lies in the inputs or the server.
func Rerun(b *Bundle) RerunResult {
	j, err := newJourney(b.Profile, b.Snap)
	if err != nil {
		return RerunResult{Error: err.Error()}
	}
	res := RerunResult{Same: true}
	for i := 0; i < len(b.Events); {
		in := b.Events[i]
		if !in.IsInput() {
			return RerunResult{Error: fmt.Sprintf("event %d has no user action before it", in.Seq)}
		}
		end := i + 1
		for end < len(b.Events) && !b.Events[end].IsInput() {
			end++
		}
		recorded := b.Events[i+1 : end]
		from := len(j.Events)
		ev := j.emit(in.Time(), in.Type, in.Target, in.Data)
		j.process(ev)
		got := j.Events[from+1:]
		for k := 0; k < len(recorded) || k < len(got); k++ {
			switch {
			case k >= len(got):
				res.Diffs = append(res.Diffs, fmt.Sprintf("After event %d: the recording has %s, the re-run has nothing.", in.Seq, recorded[k].Type))
			case k >= len(recorded):
				res.Diffs = append(res.Diffs, fmt.Sprintf("After event %d: the re-run adds %s, which the recording does not have.", in.Seq, got[k].Type))
			case recorded[k].Type != got[k].Type || string(recorded[k].Data) != string(got[k].Data) || recorded[k].Target != got[k].Target:
				res.Diffs = append(res.Diffs, fmt.Sprintf("Event %d (%s): the re-run produced a different result.", recorded[k].Seq, recorded[k].Type))
			}
		}
		// Carry on from the recorded state so one difference is reported
		// once, not at every later step.
		if len(res.Diffs) > 0 {
			res.Same = false
			j2, err := Open(b.Profile, b.Snap, b.Events[:end], b.Blobs)
			if err != nil {
				break
			}
			j = j2
		}
		i = end
		if len(res.Diffs) > 20 {
			res.Diffs = append(res.Diffs, "More differences follow.")
			break
		}
	}
	return res
}
