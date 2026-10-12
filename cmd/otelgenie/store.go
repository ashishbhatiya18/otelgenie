package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
)

// What the page remembers lives in files under the program's own folder: the
// one-time setup, unfinished projects, and for every project its recorded
// events. Nothing is kept in the browser.
//
//	data/kv.json              small named values
//	data/snapshots/COMMIT.json  each template version a project started with
//	data/journeys/ID.json     a project: its summary, events and files

var (
	storeLock sync.Mutex
	safeName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

func dataPath(parts ...string) (string, error) {
	dir, err := home()
	if err != nil {
		return "", err
	}
	for _, p := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, p)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, parts[len(parts)-1]), nil
}

func readFile(v any, parts ...string) (bool, error) {
	p, err := dataPath(parts...)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, v)
}

// writeFile replaces a file in one step, so a crash leaves the old or the new
// one, never half of either.
func writeFile(v any, parts ...string) error {
	p, err := dataPath(parts...)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

type storedJourney struct {
	Meta   map[string]any    `json:"meta"`
	Events []json.RawMessage `json:"events"`
	Blobs  map[string]string `json:"blobs"`
}

type storeRequest struct {
	Key      string          `json:"key"`
	Value    json.RawMessage `json:"value"`
	Commit   string          `json:"commit"`
	Snapshot json.RawMessage `json:"snapshot"`
	ID       string          `json:"id"`
	Meta     map[string]any  `json:"meta"`
	Delta    struct {
		Events []json.RawMessage `json:"events"`
		Blobs  map[string]string `json:"blobs"`
	} `json:"delta"`
}

func seqOf(event json.RawMessage) int {
	var e struct {
		Seq int `json:"seq"`
	}
	json.Unmarshal(event, &e)
	return e.Seq
}

// store answers one request of the page's storage layer (web/lib/db.ts).
func store(op string, payload []byte) (any, error) {
	var req storeRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
	}
	name := func(s string) error {
		if !safeName.MatchString(s) {
			return fmt.Errorf("%q cannot be used as a name", s)
		}
		return nil
	}
	storeLock.Lock()
	defer storeLock.Unlock()

	switch op {
	case "get", "set":
		kv := map[string]json.RawMessage{}
		if _, err := readFile(&kv, "data", "kv.json"); err != nil {
			return nil, err
		}
		if op == "get" {
			return map[string]any{"value": kv[req.Key]}, nil
		}
		kv[req.Key] = req.Value
		return nil, writeFile(kv, "data", "kv.json")

	case "putSnapshot":
		var s struct {
			Commit string `json:"commit"`
		}
		json.Unmarshal(req.Snapshot, &s)
		if err := name(s.Commit); err != nil {
			return nil, err
		}
		return nil, writeFile(req.Snapshot, "data", "snapshots", s.Commit+".json")

	case "getSnapshot":
		if err := name(req.Commit); err != nil {
			return nil, err
		}
		var snap json.RawMessage
		if _, err := readFile(&snap, "data", "snapshots", req.Commit+".json"); err != nil {
			return nil, err
		}
		return map[string]any{"snapshot": snap}, nil

	case "listJourneys":
		dir, err := dataPath("data", "journeys", "x")
		if err != nil {
			return nil, err
		}
		files, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), "*.json"))
		metas := []map[string]any{}
		for _, f := range files {
			var j storedJourney
			if raw, err := os.ReadFile(f); err == nil && json.Unmarshal(raw, &j) == nil && j.Meta != nil {
				metas = append(metas, j.Meta)
			}
		}
		sort.Slice(metas, func(a, b int) bool { return fmt.Sprint(metas[a]["updated"]) > fmt.Sprint(metas[b]["updated"]) })
		return map[string]any{"journeys": metas}, nil

	case "appendDelta":
		id := fmt.Sprint(req.Meta["id"])
		if err := name(id); err != nil {
			return nil, err
		}
		j := storedJourney{Blobs: map[string]string{}}
		if _, err := readFile(&j, "data", "journeys", id+".json"); err != nil {
			return nil, err
		}
		if j.Blobs == nil {
			j.Blobs = map[string]string{}
		}
		// An event that is sent twice replaces itself rather than being added again.
		at := map[int]int{}
		for i, e := range j.Events {
			at[seqOf(e)] = i
		}
		for _, e := range req.Delta.Events {
			if i, ok := at[seqOf(e)]; ok {
				j.Events[i] = e
			} else {
				j.Events = append(j.Events, e)
			}
		}
		sort.SliceStable(j.Events, func(a, b int) bool { return seqOf(j.Events[a]) < seqOf(j.Events[b]) })
		for sha, content := range req.Delta.Blobs {
			j.Blobs[sha] = content
		}
		j.Meta = req.Meta
		return nil, writeFile(j, "data", "journeys", id+".json")

	case "loadJourney":
		if err := name(req.ID); err != nil {
			return nil, err
		}
		j := storedJourney{Events: []json.RawMessage{}, Blobs: map[string]string{}}
		found, err := readFile(&j, "data", "journeys", req.ID+".json")
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("that project is no longer stored on this machine")
		}
		return j, nil

	case "deleteJourney":
		if err := name(req.ID); err != nil {
			return nil, err
		}
		p, err := dataPath("data", "journeys", req.ID+".json")
		if err != nil {
			return nil, err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown storage request %q", op)
}
