package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Inputs are the only things a user is expected to know.
type Inputs struct {
	Project    string   `json:"project"`
	Env        string   `json:"env"`
	Endpoint   string   `json:"endpoint"`
	Signals    []string `json:"signals"`
	Components []string `json:"components"`
	// Services are the project's microservices and the headers chosen from
	// their API descriptions.
	Services []Service `json:"services,omitempty"`
	// Ingress is the address at which servers outside a cluster reach the
	// cluster's collector. With it, servers send there instead of to the
	// destination, and only the cluster's collector leaves the network.
	Ingress string `json:"ingress,omitempty"`
	// Stacks are the languages the project's applications are written in:
	// java, python, go. Empty means it was not asked.
	Stacks []string `json:"stacks,omitempty"`
}

type Service struct {
	Name    string          `json:"name"`
	Headers []ServiceHeader `json:"headers,omitempty"`
	// Language picks the auto-instrumentation agent: java, python or go.
	Language string `json:"language,omitempty"`
	// Chart is what was read from the service's Helm chart, when one was given.
	Chart *ServiceChart `json:"chart,omitempty"`
}

type ServiceHeader struct {
	Name string `json:"name"`
	In   string `json:"in"`
	Kind string `json:"kind"`
}

const (
	StepDiscover = "discover"
	StepConfirm  = "confirm"
	StepReview   = "review"
	StepApply    = "apply"
	StepVerify   = "verify"
	StepDone     = "done"
)

var stepOrder = []string{StepDiscover, StepConfirm, StepReview, StepApply, StepVerify, StepDone}

func stepIndex(s string) int {
	for i, x := range stepOrder {
		if x == s {
			return i
		}
	}
	return -1
}

type ApplyResult struct {
	Result    string      `json:"result"`
	Detail    []string    `json:"detail,omitempty"`
	Diagnoses []Diagnosis `json:"diagnoses,omitempty"`
}

type VerifyRow struct {
	Component string     `json:"component"`
	Signal    string     `json:"signal"`
	Name      string     `json:"name"`
	Status    string     `json:"status"` // receiving | silent | failing | delivering | not-delivering
	Count     int        `json:"count"`
	Note      string     `json:"note,omitempty"`
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
}

type VerifyResult struct {
	Pass     bool        `json:"pass"`
	Rows     []VerifyRow `json:"rows"`
	Problems []string    `json:"problems,omitempty"`
}

// Target is one server being set up. Every field is set only by apply(), so
// the state can always be rebuilt from the event log.
type Target struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Step         string            `json:"step"`
	Facts        map[string]string `json:"facts,omitempty"`
	FactsAt      string            `json:"facts_at,omitempty"`
	Selected     []string          `json:"selected,omitempty"`
	Vars         map[string]string `json:"vars,omitempty"`
	Typed        []string          `json:"typed,omitempty"`
	Deferred     []string          `json:"deferred,omitempty"`
	WaitingSince string            `json:"waiting_since,omitempty"`
	WaitingItems []string          `json:"waiting_items,omitempty"`
	StaleAck     bool              `json:"stale_ack,omitempty"`
	Files        map[string]string `json:"files,omitempty"`
	FilesAt      string            `json:"files_at,omitempty"`
	Apply        *ApplyResult      `json:"apply,omitempty"`
	Verify       *VerifyResult     `json:"verify,omitempty"`
	Rejections   int               `json:"rejections,omitempty"`
	LastReject   string            `json:"last_reject,omitempty"`
}

type Note struct {
	TS   string `json:"ts"`
	Text string `json:"text"`
}

// Journey is one project and environment being taken to working telemetry.
type Journey struct {
	Profile Profile
	Snap    *Snapshot
	Man     Manifest
	Cat     *Catalog

	Events []Event
	Blobs  map[string]string

	In      Inputs
	Targets map[string]*Target
	Order   []string
	Notes   []Note

	newEvents []Event
	newBlobs  map[string]string
}

// Delta is what an action added. The shell must store it before showing the
// next screen, so a restart never loses or half-records a step.
type Delta struct {
	Events []Event           `json:"events"`
	Blobs  map[string]string `json:"blobs"`
}

func newJourney(p Profile, snap *Snapshot) (*Journey, error) {
	man, err := snap.Manifest()
	if err != nil {
		return nil, err
	}
	cat, problems := LoadCatalog(snap.Files)
	if len(problems) > 0 {
		return nil, fmt.Errorf("the template has problems and cannot be used:\n  %s", strings.Join(problems, "\n  "))
	}
	return &Journey{
		Profile: p, Snap: snap, Man: man, Cat: cat,
		Blobs: map[string]string{}, Targets: map[string]*Target{}, newBlobs: map[string]string{},
	}, nil
}

// Create starts a journey from the four inputs.
func Create(p Profile, snap *Snapshot, in Inputs, now time.Time) (*Journey, error) {
	j, err := newJourney(p, snap)
	if err != nil {
		return nil, err
	}
	in.Project, in.Env, in.Endpoint = strings.TrimSpace(in.Project), strings.TrimSpace(in.Env), strings.TrimSpace(in.Endpoint)
	sort.Strings(in.Signals)
	sort.Strings(in.Components)
	sort.Strings(in.Stacks)
	for _, s := range in.Stacks {
		if _, ok := languages[s]; !ok {
			return nil, fmt.Errorf("unknown application stack %q", s)
		}
	}
	var services []Service
	for _, s := range in.Services {
		if s.Name = strings.TrimSpace(s.Name); s.Name != "" {
			services = append(services, s)
		}
	}
	in.Services = services
	if in.Project == "" || in.Env == "" {
		return nil, fmt.Errorf("project and environment are both needed")
	}
	if _, _, err := endpointHostPort(in.Endpoint); err != nil {
		return nil, err
	}
	if in.Ingress = strings.TrimRight(strings.TrimSpace(in.Ingress), "/"); in.Ingress != "" {
		if _, _, err := endpointHostPort(in.Ingress); err != nil {
			return nil, fmt.Errorf("the Ingress address should look like http://203.0.113.10 or https://otel.apps.example.com")
		}
	}
	if len(in.Signals) == 0 {
		return nil, fmt.Errorf("choose at least one kind of telemetry to enable")
	}
	fp := ComputeFingerprint(p, snap, j.Man)
	j.emit(now, "in.create", "", map[string]any{"inputs": in, "fingerprint": fp, "commit": snap.Commit})
	return j, nil
}

// Open rebuilds a journey from stored events. Nothing is recomputed: the
// state is exactly what the recorded events say it is.
func Open(p Profile, snap *Snapshot, events []Event, blobs map[string]string) (*Journey, error) {
	j, err := newJourney(p, snap)
	if err != nil {
		return nil, err
	}
	if bad, reason := VerifyChain(events); bad != 0 {
		return nil, fmt.Errorf("the saved journey is damaged: %s", reason)
	}
	for k, v := range blobs {
		j.Blobs[k] = v
	}
	for _, e := range events {
		j.Events = append(j.Events, e)
		j.apply(e)
	}
	return j, nil
}

func (j *Journey) blob(content string) string {
	sha := sha256Hex(content)
	if _, ok := j.Blobs[sha]; !ok {
		j.Blobs[sha] = content
		j.newBlobs[sha] = content
	}
	return sha
}

func (j *Journey) emit(now time.Time, typ, target string, data any) Event {
	e := newEvent(j.Events, now, typ, target, data)
	j.Events = append(j.Events, e)
	j.newEvents = append(j.newEvents, e)
	j.apply(e)
	return e
}

func (j *Journey) drain() Delta {
	d := Delta{Events: j.newEvents, Blobs: j.newBlobs}
	j.newEvents, j.newBlobs = nil, map[string]string{}
	return d
}

// apply is the only place journey state changes.
func (j *Journey) apply(e Event) {
	t := j.Targets[e.Target]
	switch e.Type {
	case "in.create":
		var d struct {
			Inputs Inputs `json:"inputs"`
		}
		json.Unmarshal(e.Data, &d)
		j.In = d.Inputs
	case "in.note":
		var d struct {
			Text string `json:"text"`
		}
		json.Unmarshal(e.Data, &d)
		j.Notes = append(j.Notes, Note{TS: e.TS, Text: d.Text})
	case "d.target":
		var d struct {
			Kind string `json:"kind"`
		}
		json.Unmarshal(e.Data, &d)
		j.Targets[e.Target] = &Target{Name: e.Target, Kind: d.Kind}
		j.Order = append(j.Order, e.Target)
	}
	if t == nil {
		return
	}
	switch e.Type {
	case "d.step":
		var d struct {
			Step string `json:"step"`
		}
		json.Unmarshal(e.Data, &d)
		t.Step = d.Step
		t.Rejections, t.LastReject = 0, ""
	case "d.rejected":
		var d struct {
			Reason string `json:"reason"`
		}
		json.Unmarshal(e.Data, &d)
		t.Rejections++
		t.LastReject = d.Reason
	case "d.facts":
		var d struct {
			Facts map[string]string `json:"facts"`
		}
		json.Unmarshal(e.Data, &d)
		t.Facts, t.FactsAt, t.StaleAck = d.Facts, e.TS, false
	case "d.selection":
		var d struct {
			Components []string          `json:"components"`
			Vars       map[string]string `json:"vars"`
			Typed      []string          `json:"typed"`
		}
		json.Unmarshal(e.Data, &d)
		t.Selected, t.Vars, t.Typed = d.Components, d.Vars, d.Typed
	case "d.files":
		var d struct {
			Files map[string]string `json:"files"`
		}
		json.Unmarshal(e.Data, &d)
		t.Files, t.FilesAt = d.Files, e.TS
	case "d.waiting":
		var d struct {
			Items []string `json:"items"`
		}
		json.Unmarshal(e.Data, &d)
		t.WaitingSince, t.WaitingItems = e.TS, d.Items
	case "d.waiting_cleared":
		t.WaitingSince, t.WaitingItems = "", nil
	case "d.deferred":
		var d struct {
			Components []string `json:"components"`
		}
		json.Unmarshal(e.Data, &d)
		t.Deferred = d.Components
	case "d.stale_ack":
		t.StaleAck = true
	case "d.apply":
		var d ApplyResult
		json.Unmarshal(e.Data, &d)
		t.Apply = &d
	case "d.verify":
		var d VerifyResult
		json.Unmarshal(e.Data, &d)
		t.Verify = &d
	}
}

// Action is something the user did in the page.
type Action struct {
	Type   string         `json:"type"` // add_target | submit | redo | note | resume
	Target string         `json:"target,omitempty"`
	Step   string         `json:"step,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return Redact(x)
	case map[string]any:
		for k, vv := range x {
			x[k] = redactValue(vv)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = redactValue(vv)
		}
		return x
	}
	return v
}

// Act records what the user did, works out what follows, and returns
// everything new for the shell to store.
func (j *Journey) Act(a Action, now time.Time) (Delta, error) {
	if a.Data == nil {
		a.Data = map[string]any{}
	}
	// Redact before recording, and work only from the redacted copy, so the
	// stored log is enough to reproduce every decision.
	a.Data = redactValue(a.Data).(map[string]any)
	switch a.Type {
	case "add_target", "submit", "redo", "note", "resume":
	default:
		return Delta{}, fmt.Errorf("unknown action %q", a.Type)
	}
	if a.Type == "submit" || a.Type == "redo" {
		if j.Targets[a.Target] == nil {
			return Delta{}, fmt.Errorf("there is no server called %q in this journey", a.Target)
		}
	}
	in := j.emit(now, "in."+a.Type, a.Target, map[string]any{"step": a.Step, "data": a.Data})
	j.process(in)
	return j.drain(), nil
}

type inputPayload struct {
	Step string         `json:"step"`
	Data map[string]any `json:"data"`
}

// process derives everything that follows from one input event. Replay calls
// this with recorded inputs and compares the result with the recording.
func (j *Journey) process(in Event) {
	var p inputPayload
	json.Unmarshal(in.Data, &p)
	if p.Data == nil {
		p.Data = map[string]any{}
	}
	now := in.Time()
	switch in.Type {
	case "in.add_target":
		j.addTarget(now, str(p.Data, "name"), str(p.Data, "kind"))
	case "in.submit":
		t := j.Targets[in.Target]
		if t == nil {
			return
		}
		if p.Step != t.Step {
			j.emit(now, "d.rejected", t.Name, map[string]any{"step": t.Step, "reason": "That form belongs to an earlier step. The current step is shown below."})
		} else {
			j.submit(now, t, p.Data)
		}
		j.emitView(now, t)
	case "in.redo":
		t := j.Targets[in.Target]
		if t == nil {
			return
		}
		if i := stepIndex(p.Step); i < 0 || i > stepIndex(t.Step) {
			j.emit(now, "d.rejected", t.Name, map[string]any{"step": t.Step, "reason": "That step has not been reached yet."})
		} else {
			j.emit(now, "d.decision", t.Name, map[string]any{"step": t.Step, "text": "Went back to the " + p.Step + " step."})
			j.emit(now, "d.step", t.Name, map[string]any{"step": p.Step})
		}
		j.emitView(now, t)
	case "in.resume":
		for _, name := range j.Order {
			j.emitView(now, j.Targets[name])
		}
	}
}

func (j *Journey) addTarget(now time.Time, name, kind string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if kind == "" {
		kind = "vm"
	}
	switch {
	case !nameOK.MatchString(name):
		j.emit(now, "d.rejected", "", map[string]any{"step": "add_target", "reason": "Use a short label of lowercase letters, digits and dashes, for example db-host-1."})
	case j.Targets[name] != nil:
		j.emit(now, "d.rejected", "", map[string]any{"step": "add_target", "reason": "There is already a server with that label."})
	case kind == kindK8s && !j.hasPlatform():
		j.emit(now, "d.rejected", "", map[string]any{"step": "add_target", "reason": "This project has no kind of cluster among its components, so there is nothing to set up on one."})
	case kind != kindVM && kind != kindK8s:
		j.emit(now, "d.rejected", "", map[string]any{"step": "add_target", "reason": "Only servers (VMs) are supported in this version."})
	default:
		j.emit(now, "d.target", name, map[string]any{"kind": kind})
		t := j.Targets[name]
		j.emitFiles(now, t, j.discoverFiles(kind))
		j.emit(now, "d.step", name, map[string]any{"step": StepDiscover})
		j.emitView(now, t)
	}
}

func (j *Journey) emitFiles(now time.Time, t *Target, files map[string]string) {
	shas := map[string]string{}
	for path, content := range files {
		shas[path] = j.blob(content)
	}
	j.emit(now, "d.files", t.Name, map[string]any{"files": shas})
}

func (j *Journey) emitView(now time.Time, t *Target) {
	v := j.view(t, now)
	j.emit(now, "d.view", t.Name, map[string]any{"step": t.Step, "sha": j.blob(v.Text())})
}

func (j *Journey) decide(now time.Time, t *Target, format string, a ...any) {
	j.emit(now, "d.decision", t.Name, map[string]any{"step": t.Step, "text": fmt.Sprintf(format, a...)})
}

// flag is a decision a reviewer should notice.
func (j *Journey) flag(now time.Time, t *Target, format string, a ...any) {
	j.emit(now, "d.decision", t.Name, map[string]any{"step": t.Step, "text": fmt.Sprintf(format, a...), "flag": true})
}

func (j *Journey) reject(now time.Time, t *Target, reason string) {
	j.emit(now, "d.rejected", t.Name, map[string]any{"step": t.Step, "reason": reason})
}

func (j *Journey) goTo(now time.Time, t *Target, step string) {
	j.emit(now, "d.step", t.Name, map[string]any{"step": step})
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// lastTime is the clock for anything shown without a new action, so what is
// displayed always matches what was recorded.
func (j *Journey) lastTime() time.Time {
	if len(j.Events) == 0 {
		return time.Time{}
	}
	return j.Events[len(j.Events)-1].Time()
}

// State is what the shell renders.
type State struct {
	Inputs      Inputs      `json:"inputs"`
	Commit      string      `json:"commit"`
	SessionCode string      `json:"session_code"`
	Fingerprint Fingerprint `json:"fingerprint"`
	Targets     []*Target   `json:"targets"`
	Views       []View      `json:"views"`
	Notes       []Note      `json:"notes"`
	Events      int         `json:"events"`
	LastEvent   string      `json:"last_event"`
	// Kinds are the kinds of target this project can add.
	Kinds []string `json:"kinds"`
}

func (j *Journey) State() State {
	s := State{
		Inputs: j.In, Commit: j.Snap.Commit, SessionCode: SessionCode(j.Events),
		Fingerprint: ComputeFingerprint(j.Profile, j.Snap, j.Man),
		Notes:       j.Notes, Events: len(j.Events),
	}
	s.Kinds = []string{kindVM}
	if j.hasPlatform() {
		s.Kinds = append(s.Kinds, kindK8s)
	}
	now := j.lastTime()
	s.LastEvent = stamp(now)
	for _, name := range j.Order {
		t := j.Targets[name]
		s.Targets = append(s.Targets, t)
		s.Views = append(s.Views, j.view(t, now))
	}
	return s
}
