package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func makeTree(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{"dir/a": "A", "dir/sub/b": "B", "dir/new\nline": "N", "dir/-dash": "D", "dir/.dot": "H"}
	for n, data := range files {
		p := filepath.Join(src, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "dir/run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "dir/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(src, "dir/link")); err != nil {
		t.Fatal(err)
	}
	return src
}

// sameTree compares types, contents, symlink targets and the owner execute
// bit; other mode bits depend on the umask of the extracting tar.
func sameTree(t *testing.T, want, got string) {
	t.Helper()
	err := filepath.WalkDir(want, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(want, p)
		g := filepath.Join(got, rel)
		wi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		gi, err := os.Lstat(g)
		if err != nil {
			t.Errorf("%q missing: %v", rel, err)
			return nil
		}
		if wi.Mode().Type() != gi.Mode().Type() || wi.Mode()&0o100 != gi.Mode()&0o100 {
			t.Errorf("%q: mode %v, want %v", rel, gi.Mode(), wi.Mode())
		}
		switch {
		case wi.Mode()&fs.ModeSymlink != 0:
			wl, _ := os.Readlink(p)
			gl, _ := os.Readlink(g)
			if wl != gl {
				t.Errorf("%q: link %q, want %q", rel, gl, wl)
			}
		case wi.Mode().IsRegular():
			wb, _ := os.ReadFile(p)
			gb, _ := os.ReadFile(g)
			if !bytes.Equal(wb, gb) {
				t.Errorf("%q: content %q, want %q", rel, gb, wb)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

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
	src := makeTree(t)

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
			sameTree(t, filepath.Join(tc.dir, tc.entry), filepath.Join(out, tc.entry))
		})
	}
}
