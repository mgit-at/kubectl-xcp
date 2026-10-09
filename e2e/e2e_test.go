//go:build e2e

// Package e2e runs the kubectl-xcp binary against a kind cluster prepared by
// hack/e2e.sh.
package e2e

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mgit-at/kubectl-xcp/internal/testtree"
	"k8s.io/client-go/tools/clientcmd"
)

var bin, kubeconfig string

func TestMain(m *testing.M) {
	os.Exit(setup(m))
}

func setup(m *testing.M) int {
	kubeconfig = os.Getenv("XCP_E2E_KUBECONFIG")
	if kubeconfig == "" {
		fmt.Fprintln(os.Stderr, "XCP_E2E_KUBECONFIG is not set, use hack/e2e.sh")
		return 1
	}
	// The tests add ephemeral containers, which cannot be removed again.
	cfg, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !strings.HasPrefix(cfg.CurrentContext, "kind-") {
		fmt.Fprintf(os.Stderr, "refusing to run against context %q, only kind clusters are allowed\n", cfg.CurrentContext)
		return 1
	}

	tmp, err := os.MkdirTemp("", "xcp-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(tmp)
	bin = filepath.Join(tmp, "kubectl-xcp")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/mgit-at/kubectl-xcp").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building kubectl-xcp (did you run go generate?): %v: %s", err, out)
		return 1
	}
	for _, args := range [][]string{
		{"delete", "-f", "testdata/pods.yaml", "--ignore-not-found", "--wait"},
		{"apply", "-f", "testdata/namespace.yaml"},
		{"wait", "--for=create", "-n", "other", "serviceaccount/default", "--timeout=60s"},
		{"apply", "-f", "testdata/pods.yaml"},
		{"wait", "--for=condition=Ready", "-f", "testdata/pods.yaml", "--timeout=300s"},
	} {
		if err := kubectl(args...); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}

func kubectl(args ...string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl %v: %w: %s", args, err, out)
	}
	return nil
}

// xcp runs kubectl-xcp and returns its stderr.
func xcp(args ...string) (string, error) {
	return xcpEnv("KUBECONFIG="+kubeconfig, args...)
}

func xcpEnv(env string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env)
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

// strategy tells from the notes on stderr which strategy auto mode picked.
func strategy(stderr string) string {
	switch {
	case strings.Contains(stderr, "builtins only"):
		return "shell-builtins"
	case strings.Contains(stderr, "using an ephemeral container"):
		return "ephemeral"
	case strings.Contains(stderr, "copying file by file"):
		return "shell"
	case strings.Contains(stderr, "no usable tar"):
		return "inject"
	}
	return "exec"
}

func TestCopy(t *testing.T) {
	src := t.TempDir()
	testtree.Make(t, src)
	big := make([]byte, 5<<20)
	rand.Read(big)
	if err := os.WriteFile(filepath.Join(src, "dir/big"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		pod              string
		upload, download string
		ephemeral        int
	}{
		{"other/withtar", "exec", "exec", 0},
		{"inject", "inject", "inject", 0},
		{"nouname", "ephemeral", "shell", 1},
		{"nosh", "ephemeral", "ephemeral", 1},
	} {
		t.Run(tc.pod, func(t *testing.T) {
			xcpOK := func(want, from, to string) {
				t.Helper()
				stderr, err := xcp(from, to)
				if err != nil {
					t.Fatalf("xcp %s %s: %v: %s", from, to, err, stderr)
				}
				if got := strategy(stderr); got != want {
					t.Errorf("xcp %s %s used %s, want %s: %s", from, to, got, want, stderr)
				}
			}
			remote := tc.pod + ":"
			xcpOK(tc.upload, src+"/dir", remote+"/data/up1")
			xcpOK(tc.upload, src+"/dir/", remote+"/data/up2")
			xcpOK(tc.upload, src+"/file", remote+"/data/up3/")
			xcpOK(tc.upload, src+"/file", remote+"/data/renamed")
			xcpOK(tc.upload, src+"/file", remote+"/data/up1")

			out := t.TempDir()
			xcpOK(tc.download, remote+"/data", out+"/all")
			testtree.Equal(t, src+"/dir", out+"/all/data/up1/dir")
			testtree.Equal(t, src+"/file", out+"/all/data/up1/file")
			testtree.Equal(t, src+"/dir", out+"/all/data/up2")
			testtree.Equal(t, src+"/file", out+"/all/data/up3/file")
			testtree.Equal(t, src+"/file", out+"/all/data/renamed")
			xcpOK(tc.download, remote+"/data/up2/", out+"/contents")
			testtree.Equal(t, src+"/dir", out+"/contents")
			xcpOK(tc.download, remote+"/data/renamed", out+"/file.txt")
			testtree.Equal(t, src+"/file", out+"/file.txt")
			xcpOK(tc.download, remote+"/data/renamed", out+"/into/")
			testtree.Equal(t, src+"/file", out+"/into/renamed")

			ns, pod := "default", tc.pod
			if n, p, ok := strings.Cut(tc.pod, "/"); ok {
				ns, pod = n, p
			}
			cmd := exec.Command("kubectl", "get", "pod", "-n", ns, pod, "-o", "jsonpath={.spec.ephemeralContainers[*].name}")
			cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
			names, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if n := len(strings.Fields(string(names))); n != tc.ephemeral {
				t.Errorf("%d ephemeral containers (%s), want %d", n, names, tc.ephemeral)
			}
		})
	}
}

func TestEphemeralUID(t *testing.T) {
	out := t.TempDir()
	stderr, err := xcp("--strategy", "ephemeral", "imageuser:/pause", out+"/denied")
	if err == nil || !strings.Contains(stderr, "--uid") {
		t.Errorf("without --uid: %v, want a hint to --uid: %s", err, stderr)
	}
	if stderr, err := xcp("--strategy", "ephemeral", "--uid", "65535", "imageuser:/pause", out+"/pause"); err != nil {
		t.Fatalf("with --uid: %v: %s", err, stderr)
	}
	if fi, err := os.Stat(out + "/pause"); err != nil || fi.Size() == 0 {
		t.Errorf("pause binary not copied: %v", err)
	}
}

func TestKubeconfigFlags(t *testing.T) {
	src := t.TempDir()
	testtree.Make(t, src)
	out := t.TempDir()
	if stderr, err := xcp("-n", "other", src+"/file", "withtar:/data/nsflag"); err != nil {
		t.Fatalf("-n: %v: %s", err, stderr)
	}
	stderr, err := xcpEnv("KUBECONFIG=/nonexistent", "--kubeconfig", kubeconfig, "other/withtar:/data/nsflag", out+"/file")
	if err != nil {
		t.Fatalf("--kubeconfig: %v: %s", err, stderr)
	}
	testtree.Equal(t, src+"/file", out+"/file")
}

func TestErrors(t *testing.T) {
	out := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"other/withtar:/nope", out + "/x"}, "nope"},
		{[]string{"missing:/x", out + "/x"}, `pods "missing" not found`},
		{[]string{"--context", "nope", "other/withtar:/data", out + "/x"}, `context "nope" does not exist`},
		{[]string{"--strategy", "exec", "inject:/data", out + "/x"}, "no usable tar"},
		{[]string{"--strategy", "inject", "nosh:/data", out + "/x"}, "probing for sh"},
		{[]string{"--strategy", "shell", "nosh:/data", out + "/x"}, "no sh and cat"},
	} {
		stderr, err := xcp(tc.args...)
		if err == nil || !strings.Contains(stderr, tc.want) {
			t.Errorf("xcp %q: %v, want an error containing %q: %s", tc.args, err, tc.want, stderr)
		}
	}

	if os.Geteuid() == 0 {
		t.Log("skipping unwritable destination, root can write anywhere")
		return
	}
	ro := filepath.Join(out, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if stderr, err := xcp("other/withtar:/data", ro+"/x"); err == nil || !strings.Contains(stderr, "permission denied") {
		t.Errorf("unwritable destination: %v: %s", err, stderr)
	}
}

func TestShellBuiltins(t *testing.T) {
	src := t.TempDir()
	testtree.Make(t, src)
	big := make([]byte, 1<<20)
	rand.Read(big)
	if err := os.WriteFile(filepath.Join(src, "big"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		pod, warning string
		binarySafe   bool
	}{
		{"nocat", "WARNING: copying with bash builtins only", true},
		{"nobash", "WARNING: copying with sh builtins only: NUL bytes are silently dropped", false},
	} {
		t.Run(tc.pod, func(t *testing.T) {
			for _, from := range []string{src + "/dir", src + "/big"} {
				if stderr, err := xcp("-c", "seed", from, tc.pod+":/data/"); err != nil {
					t.Fatalf("seeding %s: %v: %s", from, err, stderr)
				}
			}
			out := t.TempDir()
			stderr, err := xcp("-c", "app", tc.pod+":/data/", out)
			if err != nil {
				t.Fatalf("%v: %s", err, stderr)
			}
			if got := strategy(stderr); got != "shell-builtins" || !strings.Contains(stderr, tc.warning) {
				t.Errorf("used %s, want shell-builtins with warning %q: %s", got, tc.warning, stderr)
			}
			testtree.Equal(t, src+"/dir", out+"/dir")
			got, err := os.ReadFile(out + "/big")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(got, big) != tc.binarySafe {
				t.Errorf("binary file intact: %v, want %v", bytes.Equal(got, big), tc.binarySafe)
			}

			stderr, err = xcp("-c", "app", src+"/file", tc.pod+":/data/up")
			if err == nil || !strings.Contains(stderr, "shareProcessNamespace") {
				t.Errorf("upload: %v, want the ephemeral container error: %s", err, stderr)
			}
		})
	}
}
