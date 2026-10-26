package pkg_test

import (
	"os"
	"testing"

	"github.com/tonis/c3pkg/internal/pkg"
)

// Runs against real libraries when C3L_SAMPLE points at one; skipped otherwise.
func TestInspectRealLibrary(t *testing.T) {
	path := os.Getenv("C3L_SAMPLE")
	if path == "" {
		t.Skip("set C3L_SAMPLE to a .c3l zip to run")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := pkg.Inspect(data, pkg.Limits{MaxCompressed: 20 << 20, MaxUnzipped: 200 << 20, MaxFiles: 5000})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", info)
}
