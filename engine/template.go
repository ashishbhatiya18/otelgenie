package engine

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Snapshot is a template repository at one commit, held entirely in memory. Journeys pin the snapshot they started with.
type Snapshot struct {
	Repo   string            `json:"repo"`
	Ref    string            `json:"ref"`
	Commit string            `json:"commit"`
	Files  map[string]string `json:"files"`
}

type Artifact struct {
	Kind string `json:"kind"`
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
	File string `json:"file,omitempty"`
	URL  string `json:"url"`
	// An "addon" is something the cluster needs that this setup does not
	// own, such as cert-manager. Name is what to call it, Check a kubectl
	// command that shows whether it is already there, and Note anything a
	// kind of cluster does differently. File is a manifest, or a folder to
	// apply with kustomize.
	Name string `json:"name,omitempty"`
	// Language marks an image as the agent for one application stack, so it
	// is only asked for when the project uses that stack.
	Language string `json:"language,omitempty"`
	Check    string `json:"check,omitempty"`
	Note     string `json:"note,omitempty"`
	// SkipOn names the kinds of cluster that do not need an addon.
	SkipOn []string `json:"skip_on,omitempty"`
	// BuiltinOn names the kinds of cluster that come with an addon already.
	// There it is confirmed, never installed.
	BuiltinOn []string `json:"builtin_on,omitempty"`
}

type Settings struct {
	MemoryMaxMB        int    `json:"memory_max_mb"`
	CPUQuotaPercent    int    `json:"cpu_quota_percent"`
	ServiceUser        string `json:"service_user"`
	StaleDiscoveryDays int    `json:"stale_discovery_days"`
}

type Manifest struct {
	Schema           int               `json:"schema"`
	Name             string            `json:"name"`
	CollectorVersion string            `json:"collector_version"`
	Settings         Settings          `json:"settings"`
	Artifacts        []Artifact        `json:"artifacts"`
	Reference        map[string]string `json:"reference"`
}

func (s *Snapshot) Manifest() (Manifest, error) {
	var m Manifest
	raw, ok := s.Files["otelgenie.json"]
	if !ok {
		return m, fmt.Errorf("the template has no otelgenie.json at its root")
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return m, fmt.Errorf("otelgenie.json is not valid JSON: %w", err)
	}
	if m.Schema != 1 {
		return m, fmt.Errorf("otelgenie.json has schema %d; this version understands schema 1", m.Schema)
	}
	if m.Settings.MemoryMaxMB == 0 {
		m.Settings.MemoryMaxMB = 512
	}
	if m.Settings.CPUQuotaPercent == 0 {
		m.Settings.CPUQuotaPercent = 50
	}
	if m.Settings.ServiceUser == "" {
		m.Settings.ServiceUser = "root"
	}
	if m.Settings.StaleDiscoveryDays == 0 {
		m.Settings.StaleDiscoveryDays = 3
	}
	return m, nil
}

func wanted(path string) bool {
	return path == "otelgenie.json" || strings.HasPrefix(path, "catalog/") || strings.HasPrefix(path, "scripts/")
}

// SnapshotFromFiles builds a snapshot from files already in hand (tests, and
// loading a template from a local folder).
func SnapshotFromFiles(repo, commit string, files map[string]string) *Snapshot {
	kept := map[string]string{}
	for p, c := range files {
		if wanted(p) {
			kept[p] = c
		}
	}
	return &Snapshot{Repo: repo, Ref: commit, Commit: commit, Files: kept}
}

// SnapshotFromFolder builds a snapshot from a copy of the template on the
// user's own disk. There is no commit, so the content hash stands in for one;
// two people with identical folders still get the same fingerprint.
func SnapshotFromFolder(files map[string]string) (*Snapshot, error) {
	kept := map[string]string{}
	for p, c := range files {
		// A folder picker reports paths with the folder's own name in front.
		if _, rest, ok := strings.Cut(p, "/"); ok && !wanted(p) && wanted(rest) {
			p = rest
		}
		if wanted(p) {
			kept[p] = c
		}
	}
	h := sha1.New()
	for _, p := range sortedKeys(kept) {
		fmt.Fprintf(h, "%s\x00%d\x00%s", p, len(kept[p]), kept[p])
	}
	snap := &Snapshot{Repo: "local-folder", Ref: "local", Commit: hex.EncodeToString(h.Sum(nil)), Files: kept}
	if _, err := snap.Manifest(); err != nil {
		return nil, err
	}
	return snap, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
