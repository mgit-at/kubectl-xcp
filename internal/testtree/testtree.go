// Package testtree builds and compares file trees for tests.
package testtree

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Make creates root/file and root/dir with names that are easy to get wrong:
// a newline, a leading dash, dotfiles, an empty dir, a symlink and an
// executable.
func Make(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{"file": "F", "dir/a": "A", "dir/sub/b c": "B", "dir/new\nline": "N", "dir/-dash": "D", "dir/.dot": "H"}
	for n, data := range files {
		write(t, filepath.Join(root, n), data, 0o644)
	}
	write(t, filepath.Join(root, "dir/run.sh"), "#!/bin/sh\n", 0o755)
	if err := os.Mkdir(filepath.Join(root, "dir/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(root, "dir/link")); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

// Equal compares types, contents, symlink targets and the owner execute bit
// of want and got. Other mode bits depend on the umask of whoever extracted.
func Equal(t *testing.T, want, got string) {
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
			t.Errorf("%q missing: %v", g, err)
			return nil
		}
		if wi.Mode().Type() != gi.Mode().Type() || wi.Mode()&0o100 != gi.Mode()&0o100 {
			t.Errorf("%q: mode %v, want %v", g, gi.Mode(), wi.Mode())
		}
		switch {
		case wi.Mode()&fs.ModeSymlink != 0:
			wl, _ := os.Readlink(p)
			gl, _ := os.Readlink(g)
			if wl != gl {
				t.Errorf("%q: link %q, want %q", g, gl, wl)
			}
		case wi.Mode().IsRegular():
			wb, _ := os.ReadFile(p)
			gb, _ := os.ReadFile(g)
			if !bytes.Equal(wb, gb) {
				t.Errorf("%q: content differs", g)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
