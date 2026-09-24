package nexus

import (
	"regexp"
	"testing"
)

func TestAPIVersionIsAReleaseTag(t *testing.T) {
	if v := APIVersion(); !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(v) {
		t.Fatalf("APIVersion() = %q, want vX.Y.Z (check .api-version)", v)
	}
}
