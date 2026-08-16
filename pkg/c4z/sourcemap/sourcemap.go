// Package sourcemap generates a Lua source map for a squished bundle: a JSON
// document mapping bundled line numbers back to {source index, original line},
// with module boundaries located by scanning the bundle.
//
// It is consumed by the Anvil source-map service, which uses it to resolve a
// driver's runtime stack traces and error reports to the author's original
// files. That service's expectations are the contract this format answers to.
package sourcemap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/driverforge/cli/pkg/c4z/squish"
)

// Mapping is a single bundled-line -> source location entry.
type Mapping struct {
	Source int `json:"source"`
	Line   int `json:"line"`
}

// SourceMap is the generated map document.
type SourceMap struct {
	Version     int                `json:"version"`
	File        string             `json:"file"`
	ContentHash string             `json:"contentHash"`
	GeneratedAt string             `json:"generatedAt"`
	Sources     []string           `json:"sources"`
	Mappings    map[string]Mapping `json:"mappings"`
}

const mainName = "__main__"

// Generate builds a source map for bundle, whose sources live under squishyDir.
// outputName is recorded as the map's "file" field (the bundle basename).
func Generate(sq squish.Squishy, squishyDir string, bundle []byte, outputName string, now time.Time) (*SourceMap, error) {
	sum := sha256.Sum256(bundle)
	contentHash := hex.EncodeToString(sum[:])[:16]

	type info struct {
		name  string
		rel   string
		lines int
	}
	var infos []info
	add := func(name, relPath string) error {
		n, err := countLines(filepath.Join(squishyDir, filepath.FromSlash(relPath)))
		if err != nil {
			return err
		}
		infos = append(infos, info{name: name, rel: filepath.ToSlash(filepath.Clean(relPath)), lines: n})
		return nil
	}
	for _, m := range sq.Modules {
		if err := add(m.Name, m.Path); err != nil {
			return nil, err
		}
	}
	if sq.Main != "" {
		if err := add(mainName, sq.Main); err != nil {
			return nil, err
		}
	}

	names := make([]string, 0, len(infos))
	for _, m := range sq.Modules {
		names = append(names, m.Name)
	}
	boundaries := findBoundaries(bundle, names)

	sources := make([]string, len(infos))
	mappings := make(map[string]Mapping)
	for i, in := range infos {
		sources[i] = in.rel
		start, ok := boundaries[in.name]
		if !ok {
			continue
		}
		for sl := 1; sl <= in.lines; sl++ {
			mappings[strconv.Itoa(start+sl-1)] = Mapping{Source: i, Line: sl}
		}
	}

	return &SourceMap{
		Version:     1,
		File:        outputName,
		ContentHash: contentHash,
		GeneratedAt: now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Sources:     sources,
		Mappings:    mappings,
	}, nil
}

// JSON renders the map as indented JSON.
func (m *SourceMap) JSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// countLines strips a single trailing newline, then counts newline-separated
// segments (so an empty file is 1 line). The backend's mappings are indexed
// against this rule, so it cannot change independently of them.
func countLines(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("count lines %s: %w", path, err)
	}
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	return bytes.Count(data, []byte{'\n'}) + 1, nil
}

// findBoundaries locates, for each module, the bundled line where its content
// begins (the line after its package.preload wrapper), and the main section's
// start (the line after the last " end)" close). It reads the bundle rather than
// tracking offsets during bundling because the map is generated from the finished
// artifact, which is also what the service resolving traces against it sees.
func findBoundaries(bundle []byte, moduleNames []string) map[string]int {
	lines := strings.Split(string(bundle), "\n")
	b := make(map[string]int)
	for i, line := range lines {
		for _, name := range moduleNames {
			if strings.Contains(line, "package.preload['"+name+"']") {
				b[name] = i + 2 // +1 to 1-index, +1 for the line after the wrapper
				break
			}
		}
	}
	lastEnd := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "end)" {
			lastEnd = i + 1
		}
	}
	if lastEnd > 0 && lastEnd < len(lines) {
		b[mainName] = lastEnd + 1
	}
	return b
}
