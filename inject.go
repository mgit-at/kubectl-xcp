package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"
)

// Named after `uname -m` in the container.
//go:generate env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o helper/bin/x86_64 ./helper
//go:generate env CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o helper/bin/aarch64 ./helper

//go:embed helper/bin
var helpers embed.FS

// Prints the architecture, then either "+DIR" for a helper left by an earlier
// run, or the writable candidate directories.
const probeScript = `uname -m || exit
n=$1; shift
for d; do
	[ -x "$d/$n" ] && echo "+$d" && exit
	[ -d "$d" ] && [ -w "$d" ] && echo "$d"
done
true`

// Running the helper weeds out noexec mounts.
const pushScript = `cat > "$1" && chmod 700 "$1" && "$1" -h 2>/dev/null || { rm -f "$1"; exit 1; }`

// inject copies a static tar helper into a writable and executable directory
// of the target container and uses it as tar.
func (r *remote) inject(ctx context.Context, pod *corev1.Pod) error {
	// Only scratch space: the helper is left behind for later runs and must
	// not end up on persistent volumes.
	dirs := []string{"/tmp", "/var/tmp", "/dev/shm"}
	emptyDirs := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		emptyDirs[v.Name] = v.EmptyDir != nil
	}
	for _, c := range pod.Spec.Containers {
		if c.Name != r.container {
			continue
		}
		for _, m := range c.VolumeMounts {
			if emptyDirs[m.Name] && !m.ReadOnly {
				dirs = append(dirs, m.MountPath)
			}
		}
	}

	name, err := helperName()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := r.exec(ctx, append([]string{"sh", "-c", probeScript, "sh", name}, dirs...), nil, &out, io.Discard); err != nil {
		return fmt.Errorf("probing for sh, uname and a writable directory: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, d := range lines[1:] {
		if d, ok := strings.CutPrefix(d, "+"); ok {
			r.tar = []string{path.Join(d, name)}
			return nil
		}
	}
	bin, err := helpers.ReadFile(path.Join("helper/bin", lines[0]))
	if err != nil {
		return fmt.Errorf("no helper for architecture %q", lines[0])
	}
	for _, d := range lines[1:] {
		p := path.Join(d, name)
		err := r.exec(ctx, []string{"sh", "-c", pushScript, "sh", p}, bytes.NewReader(bin), io.Discard, io.Discard)
		if err == nil {
			r.tar = []string{p}
			return nil
		}
		var ee utilexec.ExitError
		if !errors.As(err, &ee) {
			return fmt.Errorf("copying helper to %s: %w", p, err)
		}
	}
	return fmt.Errorf("no writable and executable directory among %v", dirs)
}

// helperName changes with the helper binaries, so outdated helpers left in a
// container are not reused.
func helperName() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(helpers, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := helpers.ReadFile(p)
		h.Write(b)
		return err
	})
	if err != nil {
		return "", err
	}
	return ".kubectl-xcp-" + hex.EncodeToString(h.Sum(nil))[:16], nil
}
