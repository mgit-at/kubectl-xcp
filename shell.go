package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/mgit-at/kubectl-xcp/internal/archive"
)

// Lists $2 below $1 as NUL-terminated records, named like tar would name
// them: d<dir>, f<file>, x<executable file> or l<symlink> followed by its target.
const listScript = `cd "$1" || exit
[ -e "$2" ] || [ -L "$2" ] || { echo "$2: No such file or directory" >&2; exit 1; }
walk() {
	if [ -L "$1" ]; then
		if command -v readlink >/dev/null; then
			printf 'l%s\0%s\0' "$1" "$(readlink "$1")"
		else
			echo "xcp: skipping symlink $1: no readlink" >&2
		fi
	elif [ -d "$1" ]; then
		printf 'd%s\0' "$1"
		for f in "$1"/* "$1"/.[!.]* "$1"/..?*; do
			if [ -e "$f" ] || [ -L "$f" ]; then walk "$f"; fi
		done
	elif [ -f "$1" ] && [ -x "$1" ]; then
		printf 'x%s\0' "$1"
	elif [ -f "$1" ]; then
		printf 'f%s\0' "$1"
	else
		echo "xcp: skipping $1: not a regular file" >&2
	fi
}
walk "$2"`

// Read a file with shell builtins only. bash can split at NUL bytes and write
// them back; LC_ALL=C keeps it from dropping bytes invalid in the locale.
const bashRead = `LC_ALL=C
while IFS= read -r -d '' c; do printf '%s\0' "$c"; done <"$1"
printf '%s' "$c"`

// POSIX read cannot represent NUL bytes and silently drops them.
const shRead = `while IFS= read -r l; do printf '%s\n' "$l"; done <"$1"
printf '%s' "$l"`

// builtins sets up copying out with nothing but a shell in the target, for
// containers without cat where no ephemeral container can be added.
func (r *remote) builtins(ctx context.Context, target string) error {
	r.container, r.tar, r.procRoot = target, nil, false
	if err := r.exec(ctx, []string{"bash", "-c", ":"}, nil, io.Discard, io.Discard); err == nil {
		r.shell, r.readFile = "bash", []string{"bash", "-c", bashRead, "bash"}
		fmt.Fprintln(os.Stderr, "xcp: WARNING: copying with bash builtins only: slow, modes are approximated, times are not preserved, and symlinks are skipped without readlink")
		return nil
	}
	if err := r.exec(ctx, []string{"sh", "-c", ":"}, nil, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("no shell in container %q: %w", target, err)
	}
	r.shell, r.readFile = "sh", []string{"sh", "-c", shRead, "sh"}
	fmt.Fprintln(os.Stderr, "xcp: WARNING: copying with sh builtins only: NUL bytes are silently dropped, so binary files are corrupted (text files are fine); also slow, modes are approximated, times are not preserved, and symlinks are skipped without readlink")
	return nil
}

// shellDownload copies src out of a container file by file, listing with the
// shell and reading each file with readFile. Modes are approximated and
// times are not preserved.
func (r *remote) shellDownload(ctx context.Context, src, dst string) error {
	dir, name := path.Dir(src), path.Base(src)
	if strings.HasSuffix(src, "/") {
		dir, name = src, "."
	}
	var list bytes.Buffer
	if err := r.exec(ctx, []string{r.shell, "-c", listScript, r.shell, dir, name}, nil, &list, os.Stderr); err != nil {
		return fmt.Errorf("listing %s: %w", src, err)
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(r.shellTar(ctx, pw, dir, list.String()))
	}()
	err := archive.Extract(pr, dst)
	if err == nil {
		_, err = io.Copy(io.Discard, pr)
	}
	pr.CloseWithError(err)
	return err
}

// shellTar writes the listed files as a tar stream. Each file is buffered in
// a temp file first, as its size must be known before its content.
func (r *remote) shellTar(ctx context.Context, w io.Writer, dir, list string) error {
	tmp, err := os.CreateTemp("", "kubectl-xcp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	tw := tar.NewWriter(w)
	now := time.Now()
	fields := strings.Split(strings.TrimSuffix(list, "\x00"), "\x00")
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			return errors.New("unexpected empty entry in listing")
		}
		typ, name := fields[i][0], fields[i][1:]
		hdr := &tar.Header{Name: name, Mode: 0o644, ModTime: now}
		var content io.Reader
		switch typ {
		case 'd':
			hdr.Typeflag, hdr.Mode, hdr.Name = tar.TypeDir, 0o755, name+"/"
		case 'l':
			i++
			if i == len(fields) || fields[i] == "" {
				return fmt.Errorf("%s: cannot read symlink, is readlink missing?", name)
			}
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, fields[i]
		case 'f', 'x':
			if typ == 'x' {
				hdr.Mode = 0o755
			}
			if err := tmp.Truncate(0); err != nil {
				return err
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return err
			}
			p := path.Join(dir, name)
			if !path.IsAbs(p) {
				p = "./" + p
			}
			if err := r.exec(ctx, append(slices.Clone(r.readFile), p), nil, tmp, os.Stderr); err != nil {
				return fmt.Errorf("reading %s: %w", p, err)
			}
			if hdr.Size, err = tmp.Seek(0, io.SeekCurrent); err != nil {
				return err
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return err
			}
			hdr.Typeflag, content = tar.TypeReg, tmp
		default:
			return fmt.Errorf("unexpected entry %q in listing", fields[i])
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if content != nil {
			if _, err := io.CopyN(tw, content, hdr.Size); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}
