package scaffold

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/clierr"
)

// DefaultRepo is the public repository the official templates live in, and
// DefaultTemplate the template `driverforge new` uses when none is named.
const (
	DefaultRepo     = "driverforge/control4-templates"
	DefaultTemplate = "driver"
)

const (
	defaultBaseURL = "https://codeload.github.com"
	maxArchive     = 64 << 20 // compressed download
	maxFileSize    = 16 << 20
	maxTotalSize   = 128 << 20
	maxFiles       = 5000
)

var fetchClient = &http.Client{Timeout: 2 * time.Minute}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)

// Source names a template repository at a ref (branch or tag). An empty Ref is
// the repository's default branch. A GitHub repository (Owner and Repo) is
// downloaded as an archive; anything else (URL) is cloned with git.
type Source struct {
	Owner, Repo string
	URL         string // git clone URL, for repositories not on github.com
	Ref         string
}

func (s Source) String() string {
	if s.URL != "" {
		if s.Ref != "" {
			return s.URL + "#" + s.Ref
		}
		return s.URL
	}
	out := s.Owner + "/" + s.Repo
	if s.Ref != "" {
		out += "@" + s.Ref
	}
	return out
}

// scpLikeRe matches git's scp-style remote, git@host:path/to/repo.git.
var scpLikeRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

// gitSchemes are the URL schemes `new` will clone from.
var gitSchemes = map[string]bool{"https": true, "http": true, "ssh": true, "git": true, "file": true}

// ParseSource accepts:
//
//	owner/repo, owner/repo@ref          a GitHub repository, downloaded as an archive
//	https://github.com/owner/repo       the same, with optional .git and /tree/<ref>
//	any other git URL, with #ref        cloned with git: https://, ssh://, git://,
//	                                    file://, or git@host:path (GitLab, Bitbucket,
//	                                    self-hosted, private repositories)
//
// A ref is a branch or tag.
func ParseSource(s string) (Source, error) {
	bad := func() (Source, error) {
		return Source{}, clierr.User(fmt.Sprintf("%q isn't a repository driverforge can use", s),
			"use owner/repo (GitHub), or a git URL such as https://gitlab.com/group/repo.git or git@host:group/repo.git; add #ref for a branch or tag")
	}
	s = strings.TrimSpace(s)
	okRef := func(ref string) bool {
		return !strings.Contains(ref, "..") && !strings.ContainsAny(ref, " \\?#~^:") && !strings.HasPrefix(ref, "-")
	}
	if strings.HasPrefix(s, "-") {
		return bad()
	}

	u, uerr := url.Parse(s)
	switch {
	case uerr == nil && u.Scheme != "" && (u.Host != "" || u.Scheme == "file"):
		if !gitSchemes[u.Scheme] {
			return bad()
		}
		ref := u.Fragment
		u.Fragment = ""
		if !okRef(ref) {
			return bad()
		}
		if h := strings.TrimPrefix(u.Host, "www."); (u.Scheme == "https" || u.Scheme == "http") && h == "github.com" {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 4 && parts[2] == "tree" {
				ref = strings.Join(parts[3:], "/")
			} else if len(parts) != 2 {
				return bad()
			}
			s = parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
			if !repoRe.MatchString(s) || !okRef(strings.ReplaceAll(ref, "/", "_")) {
				return bad()
			}
			owner, repo, _ := strings.Cut(s, "/")
			return Source{Owner: owner, Repo: repo, Ref: ref}, nil
		}
		return Source{URL: u.String(), Ref: ref}, nil
	case scpLikeRe.MatchString(strings.SplitN(s, "#", 2)[0]):
		repo, ref, _ := strings.Cut(s, "#")
		if !okRef(ref) {
			return bad()
		}
		return Source{URL: repo, Ref: ref}, nil
	}

	ref := ""
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s, ref = s[:i], s[i+1:]
	}
	if !repoRe.MatchString(s) || !okRef(ref) {
		return bad()
	}
	owner, repo, _ := strings.Cut(s, "/")
	return Source{Owner: owner, Repo: repo, Ref: ref}, nil
}

// baseURL is the archive host, overridable for tests and self-hosting.
func baseURL() string {
	if u := os.Getenv("DRIVERFORGE_TEMPLATES_BASE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultBaseURL
}

// Repo is a downloaded template repository.
type Repo struct {
	Source Source
	files  []File // paths relative to the repository root
}

// Fetch downloads the repository: a GitHub repository as an archive (public
// repositories only), anything else with git, using the user's own git
// credentials.
func Fetch(ctx context.Context, src Source) (*Repo, error) {
	if src.URL != "" {
		files, err := clone(ctx, src)
		if err != nil {
			return nil, err
		}
		return &Repo{Source: src, files: files}, nil
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	u := fmt.Sprintf("%s/%s/%s/tar.gz/%s", baseURL(), src.Owner, src.Repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("template download: %w", err)
	}
	resp, err := fetchClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, clierr.WithCode(clierr.Wrap(err, "couldn't download the template from github.com",
			"check your connection and try again"), clierr.CodeServiceUnavailable)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, clierr.User(fmt.Sprintf("couldn't find %s on GitHub", src),
			"check the repository and ref — only public repositories are supported")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("template download: %s returned %s", src, resp.Status)
	}
	files, err := extract(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return nil, err
	}
	return &Repo{Source: src, files: files}, nil
}

// extract reads a GitHub archive: a gzipped tar whose entries all sit under one
// top-level directory, which is stripped. Only regular files are kept; links,
// devices and paths that escape the archive are ignored or rejected.
func extract(r io.Reader) ([]File, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("template download: not a gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	var (
		files []File
		total int64
	)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("template download: reading archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue // directories carry nothing; symlinks are never followed
		}
		clean := path.Clean(h.Name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("template download: archive entry %q escapes the repository", h.Name)
		}
		_, rel, ok := strings.Cut(clean, "/")
		if !ok || !safeRel(rel) {
			continue
		}
		if h.Size > maxFileSize {
			return nil, clierr.User(fmt.Sprintf("template file %s is too large", rel), "templates are limited to small project files")
		}
		if total += h.Size; total > maxTotalSize || len(files) >= maxFiles {
			return nil, clierr.User("the template repository is too large", "point --repo at a smaller repository")
		}
		data, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return nil, fmt.Errorf("template download: reading %s: %w", rel, err)
		}
		mode := fs.FileMode(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		files = append(files, File{Path: rel, Data: data, Mode: mode})
	}
}

// Template is one template picked out of a repository.
type Template struct {
	Path     string // directory within the repository ("" for the root)
	Manifest Manifest
	Files    []File // relative to the template root, manifest included
}

// Select picks a template out of the repository. A named template is that
// directory; with no name, a repository whose root holds a template.json is
// itself the template, and otherwise the official default is used.
func (r *Repo) Select(name string) (Template, error) {
	dir := strings.Trim(name, "/")
	if name == "" {
		dir = DefaultTemplate
		if r.has("") {
			dir = ""
		}
	}
	if dir != "" && !safeRel(dir) {
		return Template{}, clierr.User(fmt.Sprintf("%q isn't a template directory", name), "name a directory in the repository, such as driver")
	}
	var t Template
	t.Path = dir
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	for _, f := range r.files {
		if rel, ok := strings.CutPrefix(f.Path, prefix); ok {
			t.Files = append(t.Files, File{Path: rel, Data: f.Data, Mode: f.Mode})
		}
	}
	if len(t.Files) == 0 {
		return Template{}, clierr.User(fmt.Sprintf("%s has no template %q", r.Source, dir),
			"available: "+strings.Join(r.names(), ", "))
	}
	for _, f := range t.Files {
		if f.Path == ManifestName {
			m, err := ParseManifest(f.Data)
			if err != nil {
				return Template{}, clierr.Wrap(err, "the template's "+ManifestName+" is invalid", "ask the template's author to fix it")
			}
			t.Manifest = m
		}
	}
	return t, nil
}

// has reports whether the directory holds a template.json ("" is the root).
func (r *Repo) has(dir string) bool {
	want := ManifestName
	if dir != "" {
		want = dir + "/" + ManifestName
	}
	for _, f := range r.files {
		if f.Path == want {
			return true
		}
	}
	return false
}

// names lists the top-level directories holding a template.json.
func (r *Repo) names() []string {
	var out []string
	for _, f := range r.files {
		if d, ok := strings.CutSuffix(f.Path, "/"+ManifestName); ok && !strings.Contains(d, "/") {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return []string{"(none declare a " + ManifestName + ")"}
	}
	return out
}

// Write creates dir and writes the rendered files into it. dir must not exist,
// or be empty. If writing fails part-way, the directory is removed again when
// this call created it.
func Write(dir string, files []File) (err error) {
	created := false
	switch entries, rerr := os.ReadDir(dir); {
	case errors.Is(rerr, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
		created = true
	case rerr != nil:
		return fmt.Errorf("reading %s: %w", dir, rerr)
	case len(entries) > 0:
		return clierr.User(fmt.Sprintf("%s already exists and isn't empty", dir), "pick a different name, or remove the directory first")
	}
	defer func() {
		if err != nil && created {
			os.RemoveAll(dir)
		}
	}()
	for _, f := range files {
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, f.Data, f.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", dst, err)
		}
	}
	return nil
}
