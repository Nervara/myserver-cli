package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func TestBuildSourceManifest_HashesDeployableFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "public", "logo.png"), "png")
	mustWrite(t, filepath.Join(dir, "node_modules", "huge.js"), "nope")
	mustWrite(t, filepath.Join(dir, ".env"), "SECRET=1\n")

	manifest, err := buildSourceManifest(dir)
	if err != nil {
		t.Fatalf("buildSourceManifest: %v", err)
	}

	got := manifest.Paths()
	want := []string{"main.go", "public/logo.png"}
	if !slices.Equal(got, want) {
		t.Fatalf("manifest paths = %v, want %v", got, want)
	}
	if manifest.Hash == "" {
		t.Fatal("manifest hash is empty")
	}
	if manifest.Files[0].SHA256 == "" || manifest.Files[0].Size == 0 {
		t.Fatalf("file metadata not populated: %+v", manifest.Files[0])
	}
}

func TestWriteSourceSyncTar_IncludesOnlyRequestedFilesAndMetadata(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "public", "logo.png"), "png")

	manifest, err := buildSourceManifest(dir)
	if err != nil {
		t.Fatalf("buildSourceManifest: %v", err)
	}

	var buf bytes.Buffer
	if err := writeSourceSyncTar(dir, manifest, []string{"main.go"}, []string{"old.css"}, &buf); err != nil {
		t.Fatalf("writeSourceSyncTar: %v", err)
	}

	names := gzipTarNames(t, &buf)
	sort.Strings(names)
	want := []string{".myserver-sync-delete", ".myserver-sync-manifest.json", "main.go"}
	if !slices.Equal(names, want) {
		t.Fatalf("sync tar entries = %v, want %v", names, want)
	}
}

func gzipTarNames(t *testing.T, r io.Reader) []string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}
