package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// TargetArchive packs a server's files into a .tar.gz the user copies across.
func (j *Journey) TargetArchive(target string) ([]byte, string, error) {
	t := j.Targets[target]
	if t == nil || len(t.Files) == 0 {
		return nil, "", fmt.Errorf("there are no files for %q yet", target)
	}
	mtime, _ := time.Parse(time.RFC3339, t.FilesAt)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	root := j.archiveName(t)
	for _, path := range sortedKeys(t.Files) {
		content := j.Blobs[t.Files[path]]
		mode := int64(0o644)
		switch {
		case strings.HasSuffix(path, ".sh"):
			mode = 0o755
		case path == "otelcol.env":
			mode = 0o600
		}
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + path, Mode: mode, Size: int64(len(content)), ModTime: mtime}); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), root + ".tar.gz", nil
}

type BundleMeta struct {
	SessionCode string      `json:"session_code"`
	ExportedAt  string      `json:"exported_at"`
	AppVersion  string      `json:"app_version"`
	Browser     string      `json:"browser"`
	Fingerprint Fingerprint `json:"fingerprint"`
	Events      int         `json:"events"`
}

// Bundle is everything needed to replay a journey on someone else's machine:
// the log, every file and paste it refers to, the setup, and the template it
// was pinned to. It holds internal host names, so it stays inside the
// organisation.
type Bundle struct {
	Meta    BundleMeta
	Profile Profile
	Snap    *Snapshot
	Events  []Event
	Blobs   map[string]string
}

const AppVersion = "0.1.0"

func (j *Journey) ExportBundle(now time.Time, browser string) ([]byte, string, error) {
	meta := BundleMeta{
		SessionCode: SessionCode(j.Events), ExportedAt: stamp(now), AppVersion: AppVersion, Browser: browser,
		Fingerprint: ComputeFingerprint(j.Profile, j.Snap, j.Man), Events: len(j.Events),
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name string, content []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(content)
		return err
	}
	putJSON := func(name string, v any) error {
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return put(name, raw)
	}
	var lines bytes.Buffer
	for _, e := range j.Events {
		raw, _ := json.Marshal(e)
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	steps := []func() error{
		func() error { return putJSON("meta.json", meta) },
		func() error { return putJSON("profile.json", j.Profile) },
		func() error { return putJSON("snapshot.json", j.Snap) },
		func() error { return put("events.jsonl", lines.Bytes()) },
	}
	for _, sha := range sortedKeys(j.Blobs) {
		sha := sha
		steps = append(steps, func() error { return put("blobs/"+sha, []byte(j.Blobs[sha])) })
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, "", err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	name := fmt.Sprintf("otelgenie-%s-%s-%s.zip", j.In.Project, j.In.Env, meta.SessionCode)
	return buf.Bytes(), name, nil
}

func LoadBundle(raw []byte) (*Bundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("this is not a support bundle: %w", err)
	}
	b := &Bundle{Blobs: map[string]string{}, Snap: &Snapshot{}}
	seen := map[string]bool{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(rc, 64*1024*1024))
		rc.Close()
		if err != nil {
			return nil, err
		}
		seen[f.Name] = true
		switch {
		case f.Name == "meta.json":
			err = json.Unmarshal(content, &b.Meta)
		case f.Name == "profile.json":
			err = json.Unmarshal(content, &b.Profile)
		case f.Name == "snapshot.json":
			err = json.Unmarshal(content, b.Snap)
		case f.Name == "events.jsonl":
			for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
				if line == "" {
					continue
				}
				var e Event
				if err = json.Unmarshal([]byte(line), &e); err != nil {
					break
				}
				b.Events = append(b.Events, e)
			}
		case strings.HasPrefix(f.Name, "blobs/"):
			b.Blobs[strings.TrimPrefix(f.Name, "blobs/")] = string(content)
		}
		if err != nil {
			return nil, fmt.Errorf("%s in the bundle is unreadable: %w", f.Name, err)
		}
	}
	for _, need := range []string{"meta.json", "profile.json", "snapshot.json", "events.jsonl"} {
		if !seen[need] {
			return nil, fmt.Errorf("this is not a support bundle: %s is missing", need)
		}
	}
	return b, nil
}
