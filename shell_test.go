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

func TestReadScripts(t *testing.T) {
	dir := t.TempDir()
	// bash in a UTF-8 locale drops the \x01 of \x7f\xe7\x01\xda unless LC_ALL=C is set.
	binary := []byte("a\x00b\x7f\xe7\x01\xda\xff\n\x00\x00end")
	text := []byte("  leading\\back\tslash\n\nno trailing newline")
	for name, data := range map[string][]byte{"binary": binary, "text": text} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		sh     []string
		script string
		files  []string
	}{
		{[]string{"bash"}, bashRead, []string{"binary", "text"}},
		{[]string{"sh"}, shRead, []string{"text"}},
		{[]string{"dash"}, shRead, []string{"text"}},
		{[]string{"busybox", "sh"}, shRead, []string{"text"}},
	} {
		if _, err := exec.LookPath(tc.sh[0]); err != nil {
			t.Logf("%s not installed", tc.sh[0])
			continue
		}
		for _, f := range tc.files {
			p := filepath.Join(dir, f)
			args := append(slices.Clone(tc.sh[1:]), "-c", tc.script, "sh", p)
			cmd := exec.Command(tc.sh[0], args...)
			cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
			got, err := cmd.Output()
			if err != nil {
				t.Fatalf("%v %s: %v", tc.sh, f, err)
			}
			want, _ := os.ReadFile(p)
			if string(got) != string(want) {
				t.Errorf("%v %s: got %q, want %q", tc.sh, f, got, want)
			}
		}
	}
}

func TestListScriptWithoutReadlink(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh installed")
	}
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-c", listScript, "sh", dir, ".")
	cmd.Env = []string{"PATH=" + t.TempDir()}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := records(string(out)); !slices.Equal(got, []string{"d."}) {
		t.Errorf("got %q, want only the directory", got)
	}
	if !strings.Contains(stderr.String(), "skipping symlink ./link") {
		t.Errorf("no warning about the skipped symlink: %q", stderr.String())
	}
}
