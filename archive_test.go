package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p, want string) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s: got %q, want %q", p, got, want)
	}
}

// remoteTar mimics `tar -c -C dir -- name` on the remote side.
func remoteTar(t *testing.T, dir, name string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := writeTar(&buf, filepath.Join(dir, name), name); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestExtractRsyncRules(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "dir/a"), "A")
	mustWrite(t, filepath.Join(src, "dir/sub/b"), "B")
	mustWrite(t, filepath.Join(src, "file"), "F")
	if err := os.Symlink("a", filepath.Join(src, "dir/link")); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	// dir: copies the directory itself.
	if err := extract(remoteTar(t, src, "dir"), filepath.Join(dst, "d1")); err != nil {
		t.Fatal(err)
	}
	mustRead(t, filepath.Join(dst, "d1/dir/sub/b"), "B")
	mustRead(t, filepath.Join(dst, "d1/dir/link"), "A")

	// dir/: copies the contents.
	if err := extract(remoteTar(t, filepath.Join(src, "dir"), "."), filepath.Join(dst, "d2")); err != nil {
		t.Fatal(err)
	}
	mustRead(t, filepath.Join(dst, "d2/sub/b"), "B")

	// file to a new name.
	if err := extract(remoteTar(t, src, "file"), filepath.Join(dst, "renamed")); err != nil {
		t.Fatal(err)
	}
	mustRead(t, filepath.Join(dst, "renamed"), "F")
	if fi, _ := os.Stat(filepath.Join(dst, "renamed")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v not preserved", fi.Mode())
	}

	// file into a directory given with a trailing slash, or an existing one.
	if err := extract(remoteTar(t, src, "file"), filepath.Join(dst, "d3")+"/"); err != nil {
		t.Fatal(err)
	}
	mustRead(t, filepath.Join(dst, "d3/file"), "F")
	if err := extract(remoteTar(t, src, "file"), filepath.Join(dst, "d1")); err != nil {
		t.Fatal(err)
	}
	mustRead(t, filepath.Join(dst, "d1/file"), "F")
}

func TestExtractRejectsEscapes(t *testing.T) {
	for name, hdrs := range map[string][]tar.Header{
		"dotdot":   {{Name: "../evil", Typeflag: tar.TypeReg}},
		"absolute": {{Name: "/tmp/evil", Typeflag: tar.TypeDir}, {Name: "/tmp/evil/x", Typeflag: tar.TypeReg}},
		"symlink": {
			{Name: "d/", Typeflag: tar.TypeDir},
			{Name: "d/l", Typeflag: tar.TypeSymlink, Linkname: "/tmp"},
			{Name: "d/l/evil", Typeflag: tar.TypeReg},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for _, h := range hdrs {
				if err := tw.WriteHeader(&h); err != nil {
					t.Fatal(err)
				}
			}
			tw.Close()
			if err := extract(&buf, filepath.Join(t.TempDir(), "out")+"/"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
