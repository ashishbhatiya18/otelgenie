package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Profile is the one-time setup: how things get into the airgapped
// environment. It contains no secrets.
type Profile struct {
	TemplateRepo   string   `json:"template_repo"`
	TemplateRef    string   `json:"template_ref"`
	PackageMethod  string   `json:"package_method"` // repo | manual
	PackageRepoURL string   `json:"package_repo_url"`
	ImageMethod    string   `json:"image_method"` // mirror | manual | none
	Registry       string   `json:"registry"`
	CABundle       string   `json:"ca_bundle"`
	OSFamilies     []string `json:"os_families"`
	Platforms      []string `json:"platforms"`
	// CurlImage is an image the cluster can pull that has curl in it, used
	// only for the one-off connectivity check from a pod. It is not part of
	// the fingerprint.
	CurlImage string `json:"curl_image,omitempty"`
}

const defaultCurlImage = "curlimages/curl"

func (p Profile) curlImage() string {
	if v := strings.TrimSpace(p.CurlImage); v != "" {
		return v
	}
	return defaultCurlImage
}

const fingerprintScheme = "v1"

var segmentOrder = []string{"template", "artifacts", "registry", "ca-bundle", "targets"}

// Fingerprint lets two people check they did the same setup by reading out a
// short code. Segments say which part differs. It detects mistakes; it is not
// a security control.
type Fingerprint struct {
	Scheme   string            `json:"scheme"`
	Code     string            `json:"code"`
	Segments map[string]string `json:"segments"`
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func shortCode(sum []byte, n int) string {
	var b strings.Builder
	var acc, bits uint
	for _, by := range sum {
		acc = acc<<8 | uint(by)
		bits += 8
		for bits >= 5 && b.Len() < n {
			bits -= 5
			b.WriteByte(crockford[(acc>>bits)&31])
		}
	}
	return b.String()
}

func hashParts(parts ...string) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// normAddr removes the differences between two spellings of the same address.
func normAddr(s string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), "/")
}

func normList(in []string) string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func ComputeFingerprint(p Profile, snap *Snapshot, m Manifest) Fingerprint {
	arts := make([]string, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		arts = append(arts, strings.Join([]string{a.Kind, a.OS, a.Arch, a.File, a.URL}, "|"))
	}
	sort.Strings(arts)
	ca := "none"
	if pem := strings.TrimSpace(strings.ReplaceAll(p.CABundle, "\r\n", "\n")); pem != "" {
		ca = sha256Hex(pem)
	}
	full := map[string][]byte{
		"template":  hashParts(snap.Repo, snap.Commit),
		"artifacts": hashParts(append([]string{m.CollectorVersion}, arts...)...),
		"registry":  hashParts(p.PackageMethod, normAddr(p.PackageRepoURL), p.ImageMethod, normAddr(p.Registry)),
		"ca-bundle": hashParts(ca),
		"targets":   hashParts(normList(p.OSFamilies), normList(p.Platforms)),
	}
	fp := Fingerprint{Scheme: fingerprintScheme, Segments: map[string]string{}}
	var all []string
	for _, name := range segmentOrder {
		fp.Segments[name] = shortCode(full[name], 4)
		all = append(all, hex.EncodeToString(full[name]))
	}
	code := shortCode(hashParts(all...), 8)
	fp.Code = code[:4] + "-" + code[4:]
	return fp
}

// DiffSegments names the segments where two fingerprints disagree. Segments
// absent from either side are skipped, so a partial reference still works.
func DiffSegments(mine, other map[string]string) []string {
	var out []string
	for _, name := range segmentOrder {
		a, okA := mine[name]
		b, okB := other[name]
		if okA && okB && a != b {
			out = append(out, name)
		}
	}
	return out
}

// ParseFingerprint reads the line a user copies from the app, for example
// "v1 7K3F-QM2X template=A3F1 artifacts=9C2E ...".
func ParseFingerprint(s string) Fingerprint {
	fp := Fingerprint{Segments: map[string]string{}}
	for _, f := range strings.Fields(strings.ToUpper(s)) {
		switch {
		case strings.HasPrefix(f, "V") && len(f) <= 3:
			fp.Scheme = strings.ToLower(f)
		case strings.Contains(f, "="):
			k, v, _ := strings.Cut(f, "=")
			fp.Segments[strings.ToLower(k)] = v
		case len(f) == 9 && f[4] == '-':
			fp.Code = f
		}
	}
	return fp
}

func (f Fingerprint) String() string {
	parts := []string{f.Scheme, f.Code}
	for _, name := range segmentOrder {
		parts = append(parts, name+"="+f.Segments[name])
	}
	return strings.Join(parts, " ")
}
