package nexus

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAPIVersionIsAReleaseTag(t *testing.T) {
	if v := APIVersion(); !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(v) {
		t.Fatalf("APIVersion() = %q, want vX.Y.Z (check .api-version)", v)
	}
}

// TestNoHandTypedSpecVersion keeps version numbers out of the docs: a typed
// spec version goes stale at the next pin bump, while APIVersion cannot. It
// scans the README and every hand-written, non-test Go file, examples
// included. Generated files, tests and the dated ADRs are exempt.
func TestNoHandTypedSpecVersion(t *testing.T) {
	version := regexp.MustCompile(`\bv?\d+\.\d+\.\d+\b`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "docs" || (path != "." && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		isDoc := path == "README.md"
		isSource := strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !strings.HasSuffix(path, ".gen.go")
		if !isDoc && !isSource {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if m := version.FindString(line); m != "" {
				t.Errorf("%s:%d: hand-typed version %q; say \"the pinned spec\" ([APIVersion]) instead", path, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
