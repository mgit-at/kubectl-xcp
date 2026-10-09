package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mgit-at/kubectl-xcp/internal/archive"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
	"k8s.io/streaming/pkg/httpstream"
)

type remote struct {
	client    kubernetes.Interface
	config    *rest.Config
	ns, pod   string
	container string
	tar       []string
	// procRoot reaches the target container's filesystem from an ephemeral container.
	procRoot bool
	// shell copies out file by file with sh and cat instead of tar.
	shell bool
}

// connect picks how to copy, least invasive first: the target's own tar, a
// tar helper injected into the target, sh and cat in the target (downloads
// only), or tar in an ephemeral container sharing its process namespace.
func connect(ctx context.Context, o *options, r *remote, upload bool) (*remote, error) {
	pod, err := r.client.CoreV1().Pods(r.ns).Get(ctx, r.pod, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	target := o.container
	if target == "" {
		target = pod.Annotations["kubectl.kubernetes.io/default-container"]
	}
	if target == "" {
		target = pod.Spec.Containers[0].Name
	}
	r.container = target

	if o.strategy == "auto" || o.strategy == "exec" {
		var err error
		for _, tar := range [][]string{{"tar"}, {"busybox", "tar"}} {
			err = r.exec(ctx, append(slices.Clone(tar), "-c", "-f", "/dev/null", "/dev/null"), nil, io.Discard, io.Discard)
			if err == nil {
				r.tar = tar
				return r, nil
			}
			// A missing binary is reported as a runtime-specific internal error,
			// so only errors that also block the fallback end the probing.
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err) || ctx.Err() != nil {
				return nil, fmt.Errorf("probing for tar in container %q: %w", target, err)
			}
		}
		if o.strategy == "exec" {
			return nil, fmt.Errorf("no usable tar in container %q: %w", target, err)
		}
		// The probe error is runtime noise like "executable file not found".
		fmt.Fprintf(os.Stderr, "xcp: no usable tar in container %q\n", target)
	}
	if o.strategy == "auto" || o.strategy == "inject" {
		err := r.inject(ctx, pod)
		if err == nil {
			return r, nil
		}
		if o.strategy == "inject" || ctx.Err() != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "xcp: cannot inject a tar helper into container %q (%v)\n", target, err)
	}
	if !upload && (o.strategy == "auto" || o.strategy == "shell") {
		err := r.exec(ctx, []string{"sh", "-c", "cat </dev/null"}, nil, io.Discard, io.Discard)
		if err == nil {
			r.shell = true
			fmt.Fprintln(os.Stderr, "xcp: copying file by file with sh and cat, modes are approximated and times not preserved")
			return r, nil
		}
		if o.strategy == "shell" || ctx.Err() != nil {
			return nil, fmt.Errorf("no sh and cat in container %q: %w", target, err)
		}
	}
	fmt.Fprintln(os.Stderr, "xcp: using an ephemeral container")
	return r, r.ephemeral(ctx, o, pod, target)
}

func (r *remote) ephemeral(ctx context.Context, o *options, pod *corev1.Pod, target string) error {
	if pod.Spec.ShareProcessNamespace != nil && *pod.Spec.ShareProcessNamespace {
		return errors.New("pod uses shareProcessNamespace, the ephemeral container cannot find the target container's filesystem")
	}
	i := slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool { return c.Name == target })
	if i < 0 {
		return fmt.Errorf("container %q not found in pod %q", target, pod.Name)
	}
	// Same user and capabilities as the target, otherwise the kernel denies
	// access to /proc/1/root.
	sc := pod.Spec.Containers[i].SecurityContext.DeepCopy()
	if o.uid >= 0 || o.gid >= 0 {
		if sc == nil {
			sc = &corev1.SecurityContext{}
		}
		if o.uid >= 0 {
			sc.RunAsUser = &o.uid
		}
		if o.gid >= 0 {
			sc.RunAsGroup = &o.gid
		}
	}

	// Ephemeral containers cannot be removed, so reuse a matching running one.
	name := ""
	for _, ec := range pod.Spec.EphemeralContainers {
		if strings.HasPrefix(ec.Name, "xcp-") && ec.TargetContainerName == target && ec.Image == o.image &&
			equality.Semantic.DeepEqual(ec.SecurityContext, sc) && running(pod, ec.Name) {
			name = ec.Name
		}
	}
	if name == "" {
		name = "xcp-" + utilrand.String(5)
		ec := corev1.EphemeralContainer{
			TargetContainerName: target,
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{
				Name:            name,
				Image:           o.image,
				Command:         []string{"sleep", "2147483647"},
				SecurityContext: sc,
			},
		}
		patch, err := json.Marshal(map[string]any{"spec": map[string]any{"ephemeralContainers": []corev1.EphemeralContainer{ec}}})
		if err != nil {
			return err
		}
		// Patch like kubectl debug, so the same RBAC rules apply.
		_, err = r.client.CoreV1().Pods(r.ns).Patch(ctx, r.pod, types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "ephemeralcontainers")
		if err != nil {
			return fmt.Errorf("adding ephemeral container: %w", err)
		}
		fmt.Fprintf(os.Stderr, "xcp: added ephemeral container %q to pod %q\n", name, r.pod)
		if err := r.waitRunning(ctx, name); err != nil {
			return err
		}
	}

	r.container, r.tar, r.procRoot = name, []string{"tar"}, true
	ok, err := r.isDir(ctx, "/")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("ephemeral container %q cannot access the filesystem of container %q, it probably runs as another user: retry with --uid/--gid", name, target)
	}
	return nil
}

func running(pod *corev1.Pod, name string) bool {
	for _, s := range pod.Status.EphemeralContainerStatuses {
		if s.Name == name {
			return s.State.Running != nil
		}
	}
	return false
}

func (r *remote) waitRunning(ctx context.Context, name string) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		pod, err := r.client.CoreV1().Pods(r.ns).Get(ctx, r.pod, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, s := range pod.Status.EphemeralContainerStatuses {
			if s.Name != name {
				continue
			}
			if s.State.Running != nil {
				return true, nil
			}
			if t := s.State.Terminated; t != nil {
				return false, fmt.Errorf("ephemeral container %q terminated: %s %s", name, t.Reason, t.Message)
			}
			if w := s.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" {
				return false, fmt.Errorf("ephemeral container %q not starting: %s %s", name, w.Reason, w.Message)
			}
		}
		return false, nil
	})
}

func (r *remote) exec(ctx context.Context, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := r.client.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(r.ns).Name(r.pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: r.container,
			Command:   cmd,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	// Same transport selection as kubectl exec.
	ws, err := remotecommand.NewWebSocketExecutor(r.config, "GET", req.URL().String())
	if err != nil {
		return err
	}
	spdy, err := remotecommand.NewSPDYExecutor(r.config, "POST", req.URL())
	if err != nil {
		return err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	return ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

// path maps a path in the target container to one usable by r's container.
func (r *remote) path(p string) string {
	if !r.procRoot {
		return p
	}
	if path.IsAbs(p) {
		return "/proc/1/root" + p
	}
	return "/proc/1/cwd/" + p
}

func (r *remote) isDir(ctx context.Context, p string) (bool, error) {
	err := r.exec(ctx, []string{"sh", "-c", `test -d "$1"`, "sh", r.path(p)}, nil, io.Discard, os.Stderr)
	var ee utilexec.ExitError
	if errors.As(err, &ee) && ee.ExitStatus() == 1 {
		return false, nil
	}
	return err == nil, err
}

func (r *remote) download(ctx context.Context, src, dst string) error {
	if r.shell {
		return r.shellDownload(ctx, src, dst)
	}
	dir, name := path.Dir(src), path.Base(src)
	if strings.HasSuffix(src, "/") {
		dir, name = src, "."
	}
	cmd := append(slices.Clone(r.tar), "-c", "-f", "-", "-C", r.path(dir), "--", name)
	pr, pw := io.Pipe()
	execErr := make(chan error, 1)
	go func() {
		err := r.exec(ctx, cmd, nil, pw, os.Stderr)
		pw.CloseWithError(err)
		execErr <- err
	}()
	err := archive.Extract(pr, dst)
	if err == nil {
		// Drain the archive padding, and receive the exit status of tar.
		_, err = io.Copy(io.Discard, pr)
	}
	pr.Close()
	// The remote error explains a failed extract better, unless the remote
	// side only failed because extract stopped reading.
	if eerr := <-execErr; eerr != nil && !errors.Is(eerr, io.ErrClosedPipe) {
		return eerr
	}
	return err
}

func (r *remote) upload(ctx context.Context, src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	name := dst
	if !strings.HasSuffix(src, "/") {
		into := fi.IsDir() || strings.HasSuffix(dst, "/")
		if !into {
			if into, err = r.isDir(ctx, dst); err != nil {
				return err
			}
		}
		if into {
			name = path.Join(dst, filepath.Base(src))
		}
	}
	// Extract relative to / (or the working directory), so tar creates missing
	// parent directories of dst.
	anchor := "."
	name = path.Clean(name)
	if path.IsAbs(name) {
		anchor, name = "/", strings.TrimPrefix(name, "/")
	} else if name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("remote path %q leaves the working directory, use an absolute path", dst)
	}

	cmd := append(slices.Clone(r.tar), "-x", "-o", "-f", "-", "-C", r.path(anchor))
	pr, pw := io.Pipe()
	werr := make(chan error, 1)
	go func() {
		err := archive.WriteTar(pw, src, name)
		pw.CloseWithError(err)
		werr <- err
	}()
	err = r.exec(ctx, cmd, pr, os.Stdout, os.Stderr)
	pr.Close()
	if err := <-werr; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	return err
}
