// Command helper is the minimal tar that kubectl-xcp injects into containers
// without one. It only understands the arguments kubectl-xcp passes to tar.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mgit-at/kubectl-xcp/internal/archive"
)

func main() {
	create := flag.Bool("c", false, "write NAME as an archive to stdout")
	extract := flag.Bool("x", false, "extract an archive from stdin")
	flag.Bool("o", false, "ignored, ownership is never restored")
	file := flag.String("f", "-", "archive, only - is supported")
	dir := flag.String("C", ".", "directory to work in")
	flag.Parse()

	var err error
	switch {
	case *file != "-":
		err = errors.New("only -f - is supported")
	case *create && flag.NArg() == 1:
		err = archive.WriteTar(os.Stdout, filepath.Join(*dir, flag.Arg(0)), flag.Arg(0))
	case *extract && flag.NArg() == 0:
		err = archive.Extract(os.Stdin, *dir+"/")
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "xcp-helper:", err)
		os.Exit(1)
	}
}
