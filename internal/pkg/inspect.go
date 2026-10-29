package pkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const maxManifestBytes = 1 << 20

// Limits bound what an uploaded .c3l may contain.
type Limits struct {
	MaxCompressed int64
	MaxUnzipped   int64
	MaxFiles      int
}

// ValidationError is a problem with an upload that is safe to show to the user.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func verr(format string, a ...any) error { return &ValidationError{fmt.Sprintf(format, a...)} }

// Manifest is the part of a library's manifest.json that the registry reads.
type Manifest struct {
	Provides string                     `json:"provides"`
	Version  any                        `json:"version"`
	Sources  []string                   `json:"sources"`
	Targets  map[string]json.RawMessage `json:"targets"`
}

// Info describes a validated .c3l archive.
type Info struct {
	Name       string   // manifest "provides"
	Version    string   // manifest "version", empty if the manifest has none
	Targets    []string // sorted platform names from the manifest
	FileCount  int
	Unzipped   int64
	Readme     []byte // the README.md at the archive root, nil if absent or not valid text
	SHA256File string // hash of the uploaded bytes
	SHA256Tree string // hash of the sorted "<sha256>  <path>" listing of the files
}

var sourceExts = []string{".c3", ".c3i", ".c3t"}

// Inspect validates a .c3l (a zip with manifest.json at its root) without extracting it to disk.
// Every failure is a *ValidationError.
func Inspect(data []byte, lim Limits) (*Info, error) {
	if int64(len(data)) > lim.MaxCompressed {
		return nil, verr("The file is larger than the %d MB upload limit.", lim.MaxCompressed>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, verr("This is not a valid .c3l (zip) archive.")
	}
	if len(zr.File) > lim.MaxFiles {
		return nil, verr("The archive has more than %d entries.", lim.MaxFiles)
	}

	type entry struct{ path, sum string }
	var files []entry
	var manifestJSON, readme []byte
	seen := map[string]bool{}
	budget := lim.MaxUnzipped
	for _, f := range zr.File {
		if msg := checkEntryPath(f.Name); msg != "" {
			return nil, verr("Unsafe entry %q in the archive: %s.", f.Name, msg)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, verr("Symbolic links are not allowed (%q).", f.Name)
		}
		clean := strings.TrimSuffix(f.Name, "/")
		if seen[clean] {
			return nil, verr("Duplicate entry %q in the archive.", clean)
		}
		seen[clean] = true
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > uint64(budget) {
			return nil, verr("The archive expands to more than %d MB.", lim.MaxUnzipped>>20)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, verr("Cannot read %q from the archive.", clean)
		}
		h := sha256.New()
		var w io.Writer = h
		var mbuf bytes.Buffer
		isReadme := readme == nil && strings.EqualFold(clean, "README.md") && f.UncompressedSize64 <= MaxReadmeBytes
		if isReadme {
			w = io.MultiWriter(h, &mbuf)
		}
		if clean == "manifest.json" {
			if f.UncompressedSize64 > maxManifestBytes {
				rc.Close()
				return nil, verr("manifest.json is too large.")
			}
			w = io.MultiWriter(h, &mbuf)
		}
		n, err := io.Copy(w, io.LimitReader(rc, budget+1))
		rc.Close()
		if err != nil {
			return nil, verr("The archive entry %q is corrupt.", clean)
		}
		if n > budget {
			return nil, verr("The archive expands to more than %d MB.", lim.MaxUnzipped>>20)
		}
		budget -= n
		if clean == "manifest.json" {
			manifestJSON = mbuf.Bytes()
		}
		if isReadme && ValidateReadme(mbuf.Bytes()) == "" {
			readme = mbuf.Bytes()
		}
		files = append(files, entry{clean, hex.EncodeToString(h.Sum(nil))})
	}

	if manifestJSON == nil {
		return nil, verr("manifest.json is missing from the root of the archive.")
	}
	var m Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, verr("manifest.json is not valid JSON.")
	}
	if msg := ValidateName(m.Provides); msg != "" {
		return nil, verr(`manifest.json "provides" is %q. %s`, m.Provides, msg)
	}
	version := ""
	switch v := m.Version.(type) {
	case nil:
	case string:
		version = v
	default:
		return nil, verr(`manifest.json "version" must be a string.`)
	}
	sources := m.Sources
	if len(sources) == 0 {
		sources = []string{"**"}
	}
	var matchers []*regexp.Regexp
	for _, s := range sources {
		if strings.HasPrefix(s, "/") || strings.Contains(s, "..") || strings.Contains(s, `\`) {
			return nil, verr(`manifest.json "sources" has an unsafe pattern %q.`, s)
		}
		matchers = append(matchers, globToRegexp(s))
	}
	hasSource := false
	for _, f := range files {
		for _, re := range matchers {
			if re.MatchString(f.path) && hasSourceExt(f.path) {
				hasSource = true
			}
		}
	}
	if !hasSource {
		return nil, verr("None of the manifest.json \"sources\" match a .c3 file in the archive.")
	}
	var targets []string
	for t := range m.Targets {
		if !targetNameRe.MatchString(t) {
			return nil, verr("manifest.json has an invalid target name %q.", t)
		}
		targets = append(targets, t)
	}
	sort.Strings(targets)

	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	tree := sha256.New()
	for _, f := range files {
		fmt.Fprintf(tree, "%s  %s\n", f.sum, f.path)
	}
	sum := sha256.Sum256(data)
	return &Info{
		Name:       m.Provides,
		Version:    version,
		Targets:    targets,
		Readme:     readme,
		FileCount:  len(files),
		Unzipped:   lim.MaxUnzipped - budget,
		SHA256File: hex.EncodeToString(sum[:]),
		SHA256Tree: hex.EncodeToString(tree.Sum(nil)),
	}, nil
}

func hasSourceExt(p string) bool {
	for _, e := range sourceExts {
		if strings.HasSuffix(p, e) {
			return true
		}
	}
	return false
}

// checkEntryPath returns what is wrong with a zip entry name, or "" if it is safe.
func checkEntryPath(name string) string {
	trimmed := strings.TrimSuffix(name, "/")
	switch {
	case trimmed == "":
		return "empty name"
	case len(name) > 300:
		return "name too long"
	case strings.HasPrefix(name, "/"):
		return "absolute path"
	case strings.Contains(name, `\`):
		return "backslash in path"
	case len(trimmed) >= 2 && trimmed[1] == ':':
		return "drive letter"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "control character in name"
		}
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "path traversal"
		}
	}
	return ""
}

// globToRegexp turns a manifest source pattern ("src/**", "**/*.c3") into an anchored regexp.
func globToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case c == '*' && strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case c == '*' && strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
