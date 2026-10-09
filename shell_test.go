package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// records splits listScript output into one string per entry, sorted because
// glob order depends on the shell's locale.
func records(out string) []string {
	var recs []string
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "l") && i+1 < len(fields) {
			recs = append(recs, fields[i]+" -> "+fields[i+1])
			i++
			continue
		}
		recs = append(recs, fields[i])
	}
	slices.Sort(recs)
	return recs
}

func TestListScript(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"t/a", "t/new\nline", "t/.dot", "t/..x", "t/-dash", "t/sub/b"} {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "t/run.sh"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "t/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "t/dangling")); err != nil {
		t.Fatal(err)
	}
	want := records("dt\x00ft/a\x00ft/new\nline\x00ft/.dot\x00ft/..x\x00ft/-dash\x00dt/sub\x00ft/sub/b\x00" +
		"xt/run.sh\x00dt/empty\x00lt/dangling\x00nowhere\x00")
	wantContents := records("d.\x00f./a\x00f./new\nline\x00f./.dot\x00f./..x\x00f./-dash\x00d./sub\x00f./sub/b\x00" +
		"x./run.sh\x00d./empty\x00l./dangling\x00nowhere\x00")

	for _, sh := range [][]string{{"sh"}, {"dash"}, {"bash"}, {"busybox", "sh"}} {
		t.Run(strings.Join(sh, " "), func(t *testing.T) {
			if _, err := exec.LookPath(sh[0]); err != nil {
				t.Skipf("%s not installed", sh[0])
			}
			list := func(args ...string) (string, error) {
				cmd := append(slices.Clone(sh), "-c", listScript, "sh")
				out, err := exec.Command(cmd[0], append(cmd[1:], args...)...).Output()
				return string(out), err
			}
			out, err := list(dir, "t")
			if err != nil {
				t.Fatal(err)
			}
			if got := records(out); !slices.Equal(got, want) {
				t.Errorf("got %q\nwant %q", got, want)
			}
			out, err = list(filepath.Join(dir, "t")+"/", ".")
			if err != nil {
				t.Fatal(err)
			}
			if got := records(out); !slices.Equal(got, wantContents) {
				t.Errorf("contents: got %q\nwant %q", got, wantContents)
			}
			// Without dotfiles the last glob matches nothing, which must not fail.
			if _, err := list(filepath.Join(dir, "t"), "sub"); err != nil {
				t.Errorf("dir without dotfiles: %v", err)
			}
			if _, err := list(dir, "missing"); err == nil {
				t.Error("missing path: expected an error")
			}
		})
	}
}
