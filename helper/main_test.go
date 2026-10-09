package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mgit-at/kubectl-xcp/internal/testtree"
)

func pipe(t *testing.T, from, to *exec.Cmd) {
	t.Helper()
	out, err := from.Output()
	if err != nil {
		t.Fatalf("%v: %v", from.Args, err)
	}
	to.Stdin = bytes.NewReader(out)
	if out, err := to.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", to.Args, err, out)
	}
}

// The helper replaces tar in containers, so both must read each other's archives.
func TestHelperCompatibleWithTar(t *testing.T) {
	tar, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("no tar installed")
	}
	helper := filepath.Join(t.TempDir(), "helper")
	if out, err := exec.Command("go", "build", "-o", helper, ".").CombinedOutput(); err != nil {
		t.Fatalf("building helper: %v: %s", err, out)
	}
	src := t.TempDir()
	testtree.Make(t, src)

	for _, tc := range []struct {
		name, dir, entry string
		create, extract  string
	}{
		{"helper to tar", src, "dir", helper, tar},
		{"tar to helper", src, "dir", tar, helper},
		{"helper to tar, contents", filepath.Join(src, "dir"), ".", helper, tar},
		{"tar to helper, contents", filepath.Join(src, "dir"), ".", tar, helper},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			pipe(t,
				exec.Command(tc.create, "-c", "-f", "-", "-C", tc.dir, "--", tc.entry),
				exec.Command(tc.extract, "-x", "-o", "-f", "-", "-C", out))
			testtree.Equal(t, filepath.Join(tc.dir, tc.entry), filepath.Join(out, tc.entry))
		})
	}
}
