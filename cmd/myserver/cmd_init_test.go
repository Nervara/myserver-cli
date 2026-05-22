package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectBuildPack(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"docker-compose wins over Dockerfile", []string{"Dockerfile", "docker-compose.yml"}, "dockercompose"},
		{"compose.yaml also recognised", []string{"compose.yaml"}, "dockercompose"},
		{"plain Dockerfile", []string{"Dockerfile"}, "dockerfile"},
		{"static site with index.html only", []string{"index.html", "styles.css"}, "static"},
		{"index.html with package.json → railpack (not static)", []string{"index.html", "package.json"}, "railpack"},
		{"node project", []string{"package.json"}, "railpack"},
		{"go project", []string{"go.mod", "main.go"}, "railpack"},
		{"python project", []string{"requirements.txt"}, "railpack"},
		{"empty dir", []string{}, "railpack"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := detectBuildPack(dir); got != tc.want {
				t.Errorf("detectBuildPack(%v) = %q, want %q", tc.files, got, tc.want)
			}
		})
	}
}

func TestSanitizeAppName(t *testing.T) {
	cases := map[string]string{
		"My Cool App!":         "my-cool-app",
		"hello-world":          "hello-world",
		"  spaces  ":           "spaces",
		"weird@@@@chars":       "weird-chars",
		"":                     "",
		"   ":                  "",
		"-leading-dash":        "leading-dash",
		"trailing.":            "trailing",
		"UPPERCASE":            "uppercase",
		"with.dots_and-stuff":  "with.dots_and-stuff",
		"a___multiple___wins":  "a___multiple___wins",
		"Running an app v2.0":  "running-an-app-v2.0",
	}
	for in, want := range cases {
		if got := sanitizeAppName(in); got != want {
			t.Errorf("sanitizeAppName(%q) = %q, want %q", in, got, want)
		}
	}
}
