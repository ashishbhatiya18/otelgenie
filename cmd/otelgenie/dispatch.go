package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"otelgenie/engine"
)

// The page asks for everything the engine does through one address,
// /api/call/METHOD, with a JSON body. Journeys that are open are kept here
// while the program runs; what is stored on disk is enough to open them again.
var (
	journeys = map[string]*engine.Journey{}
	// One request at a time: the engine's journeys are not built for two.
	engineLock sync.Mutex
)

type request struct {
	ID       string            `json:"id"`
	Repo     string            `json:"repo"`
	Ref      string            `json:"ref"`
	Token    string            `json:"token"`
	Profile  engine.Profile    `json:"profile"`
	Snapshot *engine.Snapshot  `json:"snapshot"`
	Inputs   engine.Inputs     `json:"inputs"`
	Events   []engine.Event    `json:"events"`
	Blobs    map[string]string `json:"blobs"`
	Action   engine.Action     `json:"action"`
	Now      string            `json:"now"`
	Target   string            `json:"target"`
	Path     string            `json:"path"`
	Browser  string            `json:"browser"`
	Bundle   string            `json:"bundle"`
	Reviewer string            `json:"reviewer"`
	Code     string            `json:"code"`
	Text     string            `json:"text"`
	Other    string            `json:"other"`
	Files    map[string]string `json:"files"`
}

type result map[string]any

func parseNow(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now()
	}
	return t
}

func describe(snap *engine.Snapshot) (result, error) {
	man, err := snap.Manifest()
	if err != nil {
		return nil, err
	}
	cat, problems := engine.LoadCatalog(snap.Files)
	return result{"snapshot": snap, "manifest": man, "components": cat.Components(""), "problems": problems}, nil
}

func journey(id string) (*engine.Journey, error) {
	j := journeys[id]
	if j == nil {
		return nil, fmt.Errorf("that journey is not open")
	}
	return j, nil
}

func dispatch(method string, payload []byte) (any, error) {
	var req request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	switch method {
	case "version":
		return result{"version": engine.AppVersion}, nil

	case "describeTemplate":
		return describe(req.Snapshot)

	case "fingerprint":
		man, err := req.Snapshot.Manifest()
		if err != nil {
			return nil, err
		}
		fp := engine.ComputeFingerprint(req.Profile, req.Snapshot, man)
		out := result{"fingerprint": fp, "text": fp.String()}
		if len(man.Reference) > 0 {
			out["reference_diff"] = engine.DiffSegments(fp.Segments, man.Reference)
			out["has_reference"] = true
		}
		return out, nil

	case "compare":
		mine, other := engine.ParseFingerprint(req.Text), engine.ParseFingerprint(req.Other)
		if len(other.Segments) == 0 {
			return result{"same": other.Code != "" && other.Code == mine.Code, "diff": []string{}, "code_only": true}, nil
		}
		diff := engine.DiffSegments(mine.Segments, other.Segments)
		return result{"same": len(diff) == 0, "diff": diff}, nil

	case "analyzeSpec":
		rep, err := engine.AnalyzeSpec(req.Text)
		if err != nil {
			return nil, err
		}
		return rep, nil

	case "analyzeChart":
		files := req.Files
		if req.Bundle != "" {
			raw, err := base64.StdEncoding.DecodeString(req.Bundle)
			if err != nil {
				return nil, fmt.Errorf("the file could not be read")
			}
			if files, err = engine.UnpackChart(raw); err != nil {
				return nil, err
			}
		}
		rep, err := engine.AnalyzeChart(files)
		if err != nil {
			return nil, err
		}
		return rep, nil

	case "project":
		p, err := engine.BuildProject(req.Profile, req.Snapshot, req.Inputs, parseNow(req.Now))
		if err != nil {
			return nil, err
		}
		return result{
			"process": p.Process, "name": p.Name,
			"readme":  base64.StdEncoding.EncodeToString([]byte(p.Readme)),
			"archive": base64.StdEncoding.EncodeToString(p.Archive),
		}, nil

	case "create":
		j, err := engine.Create(req.Profile, req.Snapshot, req.Inputs, parseNow(req.Now))
		if err != nil {
			return nil, err
		}
		journeys[req.ID] = j
		delta, _ := j.Act(engine.Action{Type: "resume"}, parseNow(req.Now))
		return result{"delta": delta, "state": j.State()}, nil

	case "open":
		j, err := engine.Open(req.Profile, req.Snapshot, req.Events, req.Blobs)
		if err != nil {
			return nil, err
		}
		journeys[req.ID] = j
		return result{"state": j.State()}, nil

	case "act":
		j, err := journey(req.ID)
		if err != nil {
			return nil, err
		}
		delta, err := j.Act(req.Action, parseNow(req.Now))
		if err != nil {
			return nil, err
		}
		return result{"delta": delta, "state": j.State()}, nil

	case "archive":
		j, err := journey(req.ID)
		if err != nil {
			return nil, err
		}
		raw, name, err := j.TargetArchive(req.Target)
		if err != nil {
			return nil, err
		}
		return result{"name": name, "base64": base64.StdEncoding.EncodeToString(raw)}, nil

	case "file":
		j, err := journey(req.ID)
		if err != nil {
			return nil, err
		}
		t := j.Targets[req.Target]
		if t == nil {
			return nil, fmt.Errorf("no such server")
		}
		return result{"content": j.Blobs[t.Files[req.Path]]}, nil

	case "export":
		j, err := journey(req.ID)
		if err != nil {
			return nil, err
		}
		raw, name, err := j.ExportBundle(parseNow(req.Now), req.Browser)
		if err != nil {
			return nil, err
		}
		return result{"name": name, "base64": base64.StdEncoding.EncodeToString(raw), "session_code": engine.SessionCode(j.Events)}, nil

	case "analyze":
		raw, err := base64.StdEncoding.DecodeString(req.Bundle)
		if err != nil {
			return nil, fmt.Errorf("the file could not be read")
		}
		b, err := engine.LoadBundle(raw)
		if err != nil {
			return nil, err
		}
		var reviewer *engine.Fingerprint
		if strings.TrimSpace(req.Reviewer) != "" {
			fp := engine.ParseFingerprint(req.Reviewer)
			reviewer = &fp
		}
		report := engine.Analyze(b, reviewer, req.Code)
		pdf, err := engine.RenderPDF(report)
		if err != nil {
			return nil, err
		}
		return result{
			"report": report, "pdf": base64.StdEncoding.EncodeToString(pdf),
			"pdf_name": "otelgenie-replay-" + report.Meta.SessionCode + ".pdf",
		}, nil

	case "doctor":
		var found []engine.Diagnosis
		seen := map[engine.Diagnosis]bool{}
		for _, line := range strings.Split(engine.Redact(req.Text), "\n") {
			if d := engine.Diagnose(line); d != nil && !seen[*d] {
				seen[*d] = true
				found = append(found, *d)
			}
		}
		return result{"diagnoses": found}, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}
