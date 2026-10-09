package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// WriteTar writes src as a tar stream, with src itself named name
// ("" to only write its contents).
func WriteTar(w io.Writer, src, name string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if name == "" && rel == "." {
			return nil
		}
		n := path.Join(name, filepath.ToSlash(rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		var link string
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		hdr.Name = n
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Extract unpacks a tar stream below dst. A single file is written
// as dst itself, unless dst ends in "/" or is an existing directory (rsync rules).
func Extract(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	hdr, err := tr.Next()
	if err == io.EOF {
		return errors.New("received an empty archive")
	}
	if err != nil {
		return err
	}
	dir, rename := dst, ""
	if hdr.Typeflag != tar.TypeDir && !strings.HasSuffix(dst, "/") {
		fi, err := os.Stat(dst)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err != nil || !fi.IsDir() {
			dir, rename = filepath.Dir(dst), filepath.Base(dst)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// The archive comes from the container and is untrusted: os.Root refuses
	// names and symlinks that escape dir.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for {
		name := path.Clean(hdr.Name)
		if rename != "" {
			name = rename
		}
		if err := extractEntry(root, tr, hdr, name); err != nil {
			return fmt.Errorf("extracting %s: %w", hdr.Name, err)
		}
		hdr, err = tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func extractEntry(root *os.Root, r io.Reader, hdr *tar.Header, name string) error {
	if hdr.Typeflag == tar.TypeDir {
		return root.MkdirAll(name, 0o755)
	}
	if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
		return err
	}
	switch hdr.Typeflag {
	case tar.TypeReg:
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if err := root.Chmod(name, hdr.FileInfo().Mode().Perm()); err != nil {
			return err
		}
		return root.Chtimes(name, hdr.ModTime, hdr.ModTime)
	case tar.TypeSymlink:
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return root.Symlink(hdr.Linkname, name)
	default:
		fmt.Fprintf(os.Stderr, "xcp: skipping %s: unsupported file type %q\n", hdr.Name, hdr.Typeflag)
		return nil
	}
}
