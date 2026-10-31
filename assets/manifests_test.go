package assets

import (
	"strings"
	"testing"
)

func TestRuntimeInstallationDirectoryPinsVariantAndRejectsOtherPaths(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, tc := range []struct {
		id, digest, variant, want string
	}{
		{"b11669", digest, "", "b11669"},
		{"b11669", digest, "b11669-" + digest, "b11669-" + digest},
		{"b11669", digest, "../elsewhere", ""},
		{"b11669", digest, "b11669-" + strings.Repeat("b", 64), ""},
		{"b11669", "invalid", "b11669-invalid", ""},
		{"../elsewhere", digest, "", ""},
	} {
		got, err := RuntimeInstallationDirectory(tc.id, tc.digest, tc.variant)
		if (err != nil) != (tc.want == "") || got != tc.want {
			t.Fatalf("%+v: %q %v", tc, got, err)
		}
	}
}

func TestPinnedRuntimeDirectorySelectsEmbeddedVariant(t *testing.T) {
	old := runtimeLock
	t.Cleanup(func() { runtimeLock = old })
	runtimeLock = `{"id":"b11669","sha256":"` + strings.Repeat("a", 64) + `","installation_id":"b11669-` + strings.Repeat("a", 64) + `"}`
	directory, err := RuntimeDirectory()
	if err != nil || directory != "b11669-"+strings.Repeat("a", 64) {
		t.Fatalf("runtime directory %q %v", directory, err)
	}
}
