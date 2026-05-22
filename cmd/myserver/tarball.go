package main

// Tarball packing for `myserver up`. Walks the project root, applies an
// exclusion list, writes a gzipped tar archive to a writer (typically the
// upload's multipart body).
//
// Exclusion rules are kept intentionally tiny: the same default deny list
// the bash shim used, plus any patterns from .myserverignore / .dockerignore
// when present. Full .gitignore semantics (negations, character classes,
// directory-relative patterns) would be neat — but the projects we deploy
// rarely need it, and a fancier matcher pulls in a real dependency.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// defaultExcludes covers the directories almost no app wants in a deploy
// tarball. Order matches the Go stdlib gitignore-ish matcher: a path is
// excluded if any of these components appear in its slash-separated form.
//
// `.env*` is a single glob that covers .env, .env.local, .env.production,
// .env.staging, .env.development, etc. — previously the list enumerated
// only three of those, and a .env.production file would be tarred and
// uploaded by `myserver up` (high-severity leak vector). The glob is
// matched by filepath.Match's '*' wildcard.
var defaultExcludes = []string{
	"node_modules/", "dist/", ".astro/", ".wrangler/",
	".git/", ".github/",
	".env*",
	".DS_Store",
	"*.log",
}

// loadIgnoreFile returns one pattern per non-empty, non-comment line, or
// nil if the file doesn't exist. We treat both .myserverignore and
// .dockerignore identically — neither needs full gitignore semantics for
// MVP usage, just glob-on-segment matches.
func loadIgnoreFile(name string) []string {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// pathMatched returns true when any exclusion pattern matches the given
// repo-relative path. Trailing-slash patterns match the path or any prefix
// segment ending in that name.
func pathMatched(rel string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, "/") {
			seg := strings.TrimSuffix(p, "/")
			parts := strings.Split(rel, "/")
			for _, part := range parts {
				if part == seg {
					return true
				}
			}
			continue
		}
		base := filepath.Base(rel)
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// writeTarball packs the project root into a gzipped tar stream written to
// w. Returns the number of files included and the total uncompressed bytes.
func writeTarball(root string, w io.Writer) (files int, bytes int64, err error) {
	excludes := append([]string{}, defaultExcludes...)
	for _, name := range []string{".myserverignore", filepath.Join(root, ".myserverignore"), ".dockerignore", filepath.Join(root, ".dockerignore")} {
		if patterns := loadIgnoreFile(name); patterns != nil {
			excludes = append(excludes, patterns...)
			break
		}
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	defer func() {
		if cerr := tw.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if cerr := gz.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Normalize to forward slashes for cross-platform matching + tar headers.
		rel = filepath.ToSlash(rel)
		if pathMatched(rel, excludes) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		n, err := io.Copy(tw, f)
		if err != nil {
			return err
		}
		files++
		bytes += n
		return nil
	})
	if walkErr != nil {
		return files, bytes, fmt.Errorf("pack tarball: %w", walkErr)
	}
	return files, bytes, nil
}
