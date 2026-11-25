package scaffold

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNames(t *testing.T) {
	got, err := Names("My-Pool_pump", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"name": "My-Pool_pump", "slug": "my-pool-pump", "id": "my_pool_pump", "title": "My Pool Pump", "year": "2026"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, err := Names("---", time.Now()); err == nil {
		t.Error("a name with no letters or digits should be rejected")
	}
}

func TestSubstitute(t *testing.T) {
	vars := map[string]string{"id": "pump"}
	got, err := Substitute("a ${id} $${id} {{1,2}}", vars)
	if err != nil || got != "a pump ${id} {{1,2}}" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := Substitute("${nope}", vars); err == nil {
		t.Error("unknown variable should fail")
	}
}

func TestRender(t *testing.T) {
	vars := map[string]string{"id": "pump", "title": "Pump"}
	files, err := Render([]File{
		{Path: "template.json", Data: []byte("{}")},
		{Path: "src/${id}.c4zproj", Data: []byte("<n>${title}</n>")},
		{Path: "logo.bin", Data: []byte{0, '$', '{', 'x', '}'}},
	}, vars)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "logo.bin" || files[1].Path != "src/pump.c4zproj" || string(files[1].Data) != "<n>Pump</n>" {
		t.Fatalf("unexpected render: %+v", files)
	}
	if !bytes.Equal(files[0].Data, []byte{0, '$', '{', 'x', '}'}) {
		t.Error("binary file was modified")
	}
	if _, err := Render([]File{{Path: "${id}", Data: nil}}, map[string]string{"id": "../x"}); err == nil {
		t.Error("a variable must not let a path escape the project")
	}
}

func TestResolve(t *testing.T) {
	m, err := ParseManifest([]byte(`{"variables":{"vendor":{"default":"Acme"},"model":{"required":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	vars, missing, err := Resolve(m, map[string]string{"id": "x"}, nil)
	if err != nil || vars["vendor"] != "Acme" || len(missing) != 1 || missing[0] != "model" {
		t.Fatalf("vars=%v missing=%v err=%v", vars, missing, err)
	}
	if _, _, err := Resolve(m, nil, map[string]string{"typo": "1"}); err == nil {
		t.Error("an undeclared --set should fail")
	}
	if _, err := ParseManifest([]byte(`{"variables":{"name":{}}}`)); err == nil {
		t.Error("redeclaring a built-in should fail")
	}
}

func TestParseSource(t *testing.T) {
	for in, want := range map[string]Source{
		"a/b":                              {Owner: "a", Repo: "b"},
		"a/b@v1.2":                         {Owner: "a", Repo: "b", Ref: "v1.2"},
		"https://github.com/a/b.git":       {Owner: "a", Repo: "b"},
		"https://github.com/a/b/tree/main": {Owner: "a", Repo: "b", Ref: "main"},
		"https://gitlab.com/g/s/r.git":     {URL: "https://gitlab.com/g/s/r.git"},
		"https://gitlab.com/g/r.git#v2":    {URL: "https://gitlab.com/g/r.git", Ref: "v2"},
		"git@bitbucket.org:team/r.git#dev": {URL: "git@bitbucket.org:team/r.git", Ref: "dev"},
		"ssh://git@host/r.git":             {URL: "ssh://git@host/r.git"},
		"file:///srv/r":                    {URL: "file:///srv/r"},
	} {
		got, err := ParseSource(in)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "a", "ext::sh -c evil", "-oProxy=x", "ftp://h/r", "https://h/r#a..b", "a/b@../x", "a/b/c"} {
		if _, err := ParseSource(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func archive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, entries map[string]string) {
	t.Helper()
	data := archive(t, entries)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/o/r/tar.gz/HEAD" {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_TEMPLATES_BASE_URL", srv.URL)
}

func TestFetchSelectWrite(t *testing.T) {
	serve(t, map[string]string{
		"r-main/README.md":             "repo",
		"r-main/driver/template.json":  `{"description":"Bare driver"}`,
		"r-main/driver/src/driver.lua": "-- ${title}",
		"r-main/other/template.json":   `{}`,
		"r-main/other/x.txt":           "x",
	})
	repo, err := Fetch(context.Background(), Source{Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := repo.Select("")
	if err != nil || tmpl.Path != "driver" || tmpl.Manifest.Description != "Bare driver" {
		t.Fatalf("select default: %+v, %v", tmpl, err)
	}
	if _, err := repo.Select("missing"); err == nil || !strings.Contains(err.Error(), "no template") {
		t.Errorf("missing template: %v", err)
	}
	files, err := Render(tmpl.Files, map[string]string{"title": "Pump"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "pump")
	if err := Write(dir, files); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "driver.lua"))
	if err != nil || string(got) != "-- Pump" {
		t.Fatalf("driver.lua = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err == nil {
		t.Error("template.json must not be copied")
	}
	if err := Write(dir, files); err == nil {
		t.Error("writing into a non-empty directory should fail")
	}
}

func TestFetchRepoRootTemplate(t *testing.T) {
	serve(t, map[string]string{"r-main/template.json": "{}", "r-main/driver.lua": "x"})
	repo, err := Fetch(context.Background(), Source{Owner: "o", Repo: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if tmpl, err := repo.Select(""); err != nil || tmpl.Path != "" {
		t.Fatalf("root template: %+v, %v", tmpl, err)
	}
}

func TestFetchNotFound(t *testing.T) {
	serve(t, nil)
	if _, err := Fetch(context.Background(), Source{Owner: "o", Repo: "missing"}); err == nil || !strings.Contains(err.Error(), "couldn't find") {
		t.Errorf("got %v", err)
	}
}

func TestExtractRejectsEscape(t *testing.T) {
	if _, err := extract(bytes.NewReader(archive(t, map[string]string{"r/../../evil": "x"}))); err == nil {
		t.Error("an entry escaping the repository should be rejected")
	}
}

func TestCloneLocalRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(repo, "driver"), 0o755)
	os.WriteFile(filepath.Join(repo, "driver", "template.json"), []byte(`{"description":"cloned"}`), 0o644)
	os.WriteFile(filepath.Join(repo, "driver", "driver.lua"), []byte("-- ${id}"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "init")
	git("tag", "v1")

	src, err := ParseSource("file://" + repo + "#v1")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := r.Select("")
	if err != nil || tmpl.Manifest.Description != "cloned" || len(tmpl.Files) != 2 {
		t.Fatalf("%+v, %v", tmpl, err)
	}
	for _, f := range r.files {
		if strings.HasPrefix(f.Path, ".git/") {
			t.Errorf(".git leaked into the template: %s", f.Path)
		}
	}
	bad, _ := ParseSource("file://" + repo + "#nope")
	if _, err := Fetch(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "couldn't clone") {
		t.Errorf("missing ref: %v", err)
	}
}
