package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestPathMatched(t *testing.T) {
	cases := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"main.go", []string{"*.log"}, false},
		{"server.log", []string{"*.log"}, true},
		{"node_modules/foo/bar.js", []string{"node_modules/"}, true},
		{"app/node_modules/x.js", []string{"node_modules/"}, true},
		{".env", []string{".env"}, true},
		{".env.local", []string{".env"}, false}, // exact basename match, not prefix
		// Regression — the default exclude is now `.env*` so any
		// flavour of dotenv file is kept out of upload tarballs.
		{".env", []string{".env*"}, true},
		{".env.local", []string{".env*"}, true},
		{".env.production", []string{".env*"}, true},
		{".env.staging", []string{".env*"}, true},
		{".env.development", []string{".env*"}, true},
		{".env.deploy", []string{".env*"}, true},
		// Files that merely start with "env" should NOT match.
		{"envrc.txt", []string{".env*"}, false},
		{"src/.DS_Store", []string{".DS_Store"}, true},
		{"a/b/c.ts", []string{"src/*.ts"}, false},
		{"src/a.ts", []string{"src/*.ts"}, true},
		{"any.go", []string{""}, false}, // empty patterns ignored
	}
	for _, c := range cases {
		got := pathMatched(c.path, c.patterns)
		if got != c.want {
			t.Errorf("pathMatched(%q, %v) = %v, want %v", c.path, c.patterns, got, c.want)
		}
	}
}

func TestLoadIgnoreFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, ".myserverignore")
	body := `
# comment line, ignored
node_modules/

# blank line above also ignored
*.tmp
build/
`
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := loadIgnoreFile(f)
	want := []string{"node_modules/", "*.tmp", "build/"}
	if !slices.Equal(got, want) {
		t.Errorf("loadIgnoreFile = %v, want %v", got, want)
	}

	if loadIgnoreFile(filepath.Join(dir, "missing")) != nil {
		t.Error("loadIgnoreFile(missing) should return nil")
	}
}

func TestWriteTarball_RespectsExcludes(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Dockerfile"), "FROM scratch\n")
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "server.log"), "noisy\n")
	mustWrite(t, filepath.Join(dir, ".env"), "SECRET=1\n")
	// Cover every common dotenv variant — they were silently shipping
	// to remote servers before the .env* glob landed. Worst case: a
	// developer's .env.production with prod DB creds got tarred and
	// uploaded by `myserver up`.
	mustWrite(t, filepath.Join(dir, ".env.local"), "SECRET=2\n")
	mustWrite(t, filepath.Join(dir, ".env.production"), "PROD_SECRET=3\n")
	mustWrite(t, filepath.Join(dir, ".env.staging"), "STAGE_SECRET=4\n")
	mustWrite(t, filepath.Join(dir, ".env.development"), "DEV_SECRET=5\n")
	mustWrite(t, filepath.Join(dir, "node_modules", "lib", "x.js"), "// huge\n")
	mustWrite(t, filepath.Join(dir, ".git", "HEAD"), "ref: ...\n")
	mustWrite(t, filepath.Join(dir, "src", "app.ts"), "export {};\n")

	var buf bytes.Buffer
	files, sz, err := writeTarball(dir, &buf)
	if err != nil {
		t.Fatalf("writeTarball: %v", err)
	}
	if files == 0 || sz == 0 {
		t.Fatalf("empty tarball: files=%d size=%d", files, sz)
	}

	names := tarballNames(t, &buf)
	want := []string{"Dockerfile", "main.go", "src/", "src/app.ts"}
	got := append([]string{}, names...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("tarball entries = %v, want %v (full=%v)", got, want, names)
	}

	for _, bad := range []string{"server.log", ".env", ".env.local", ".env.production", ".env.staging", ".env.development", "node_modules/", ".git/", ".git/HEAD"} {
		for _, n := range names {
			if n == bad || strings.HasPrefix(n, bad) {
				t.Errorf("excluded path %q present in tarball", n)
			}
		}
	}
}

func TestWriteTarball_HonorsIgnoreFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Dockerfile"), "FROM scratch\n")
	mustWrite(t, filepath.Join(dir, "secret.bin"), "x\n")
	mustWrite(t, filepath.Join(dir, "vendor", "junk.txt"), "x\n")
	mustWrite(t, filepath.Join(dir, ".myserverignore"), "secret.bin\nvendor/\n")

	var buf bytes.Buffer
	if _, _, err := writeTarball(dir, &buf); err != nil {
		t.Fatal(err)
	}
	names := tarballNames(t, &buf)
	for _, bad := range []string{"secret.bin", "vendor/", "vendor/junk.txt"} {
		for _, n := range names {
			if n == bad || strings.HasPrefix(n, bad) {
				t.Errorf("ignore-file pattern not respected: %q in tarball", n)
			}
		}
	}
	if !slices.Contains(names, "Dockerfile") {
		t.Errorf("Dockerfile missing from tarball: %v", names)
	}
}

func TestWriteTarball_GzipDecompresses(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "hello\n")

	var buf bytes.Buffer
	if _, _, err := writeTarball(dir, &buf); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("output is not valid gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("malformed tar: %v", err)
		}
		if hdr.Name == "a.txt" {
			body, _ := io.ReadAll(tr)
			if string(body) != "hello\n" {
				t.Errorf("file content corrupted: %q", body)
			}
			return
		}
	}
	t.Error("a.txt missing from tarball")
}

// ─── helpers ─────────────────────────────────────────────────────────

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tarballNames(t *testing.T, r io.Reader) []string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		out = append(out, hdr.Name)
	}
	return out
}
