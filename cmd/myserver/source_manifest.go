package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	sourceSyncManifestName = ".myserver-sync-manifest.json"
	sourceSyncDeleteName   = ".myserver-sync-delete"
)

type sourceManifest struct {
	Hash  string               `json:"hash"`
	Files []sourceManifestFile `json:"files"`
}

type sourceManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   int64  `json:"mode"`
}

type sourceSyncPlan struct {
	CacheReady   bool     `json:"cache_ready"`
	ManifestHash string   `json:"manifest_hash"`
	MissingPaths []string `json:"missing_paths"`
	DeletePaths  []string `json:"delete_paths"`
	ReusedFiles  int      `json:"reused_files"`
	UploadFiles  int      `json:"upload_files"`
	UploadBytes  int64    `json:"upload_bytes"`
}

func (m sourceManifest) Paths() []string {
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		out = append(out, f.Path)
	}
	return out
}

func buildSourceManifest(root string) (*sourceManifest, error) {
	excludes := sourceExcludes(root)
	var files []sourceManifestFile
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
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
		rel = filepath.ToSlash(rel)
		if pathMatched(rel, excludes) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		sum, err := sha256File(path)
		if err != nil {
			return err
		}
		files = append(files, sourceManifestFile{
			Path:   rel,
			SHA256: sum,
			Size:   info.Size(),
			Mode:   int64(info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("build source manifest: %w", err)
	}
	slices.SortFunc(files, func(a, b sourceManifestFile) int {
		return strings.Compare(a.Path, b.Path)
	})
	m := &sourceManifest{Files: files}
	hash, err := sourceManifestHash(files)
	if err != nil {
		return nil, err
	}
	m.Hash = hash
	return m, nil
}

func sourceExcludes(root string) []string {
	excludes := append([]string{}, defaultExcludes...)
	for _, name := range []string{".myserverignore", filepath.Join(root, ".myserverignore"), ".dockerignore", filepath.Join(root, ".dockerignore")} {
		if patterns := loadIgnoreFile(name); patterns != nil {
			excludes = append(excludes, patterns...)
			break
		}
	}
	return excludes
}

func sourceManifestHash(files []sourceManifestFile) (string, error) {
	body, err := json.Marshal(files)
	if err != nil {
		return "", fmt.Errorf("encode source manifest: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeSourceSyncTar(root string, manifest *sourceManifest, uploadPaths, deletePaths []string, w io.Writer) (err error) {
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

	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode source manifest: %w", err)
	}
	if err := writeTarBytes(tw, sourceSyncManifestName, manifestBody, 0o644); err != nil {
		return err
	}
	if err := writeTarBytes(tw, sourceSyncDeleteName, []byte(strings.Join(deletePaths, "\n")), 0o644); err != nil {
		return err
	}

	allowed := map[string]sourceManifestFile{}
	for _, f := range manifest.Files {
		allowed[f.Path] = f
	}
	for _, rel := range uploadPaths {
		meta, ok := allowed[rel]
		if !ok {
			return fmt.Errorf("sync path %q is not in manifest", rel)
		}
		if err := writeTarFile(tw, filepath.Join(root, filepath.FromSlash(rel)), meta); err != nil {
			return err
		}
	}
	return nil
}

func writeTarBytes(tw *tar.Writer, name string, body []byte, mode int64) error {
	hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(body)), ModTime: time.Unix(0, 0)}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func writeTarFile(tw *tar.Writer, path string, meta sourceManifestFile) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := &tar.Header{Name: meta.Path, Mode: meta.Mode, Size: meta.Size}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
