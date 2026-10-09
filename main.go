package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
)

type options struct {
	flags     *genericclioptions.ConfigFlags
	container string
	strategy  string
	image     string
	uid, gid  int64
}

func main() {
	o := &options{flags: genericclioptions.NewConfigFlags(true)}
	cmd := &cobra.Command{
		Use:   "kubectl xcp [flags] SRC DST",
		Short: "Copy files and directories to and from containers, even without tar in them",
		Long: `Copy files and directories to and from containers, even without tar in them.

One of SRC and DST is a remote path written as [NAMESPACE/]POD:PATH.
Paths follow the rsync convention:
  dir/  copies the contents of dir
  dir   copies dir itself
  DST/  is always a directory and is created if missing

If the container has no tar, a small tar helper is copied into it (needs sh,
cat, chmod and uname there). Failing that, copies from the container use sh and
cat file by file. As a last resort, an ephemeral container with tar is added to
the pod and reaches the container's filesystem through /proc/1/root.`,
		Example: `  kubectl xcp ./conf/ mypod:/etc/app/      # contents of conf into /etc/app
  kubectl xcp mypod:/data ./backup         # creates ./backup/data
  kubectl xcp -c app ns/mypod:/etc/app.yaml app.yaml`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), o, args[0], args[1])
		},
	}
	o.flags.AddFlags(cmd.Flags())
	cmd.Flags().StringVarP(&o.container, "container", "c", "", "container name, defaults to the pod's default container")
	cmd.Flags().StringVar(&o.strategy, "strategy", "auto", "auto, exec (tar in the container), inject (copy a tar helper into the container), shell (sh and cat in the container, only from it) or ephemeral (tar in an ephemeral container)")
	cmd.Flags().StringVar(&o.image, "image", "busybox:1.37", "image for the ephemeral container, must contain tar and sh")
	cmd.Flags().Int64Var(&o.uid, "uid", -1, "user ID for the ephemeral container, must match the target container's")
	cmd.Flags().Int64Var(&o.gid, "gid", -1, "group ID for the ephemeral container")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := cmd.ExecuteContext(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, o *options, src, dst string) error {
	switch o.strategy {
	case "auto", "exec", "inject", "shell", "ephemeral":
	default:
		return fmt.Errorf("unknown strategy %q", o.strategy)
	}
	srcPod, srcPath := splitRemote(src)
	dstPod, dstPath := splitRemote(dst)
	if (srcPod == "") == (dstPod == "") {
		return errors.New("exactly one of SRC and DST must be [NAMESPACE/]POD:PATH")
	}
	if o.strategy == "shell" && dstPod != "" {
		return errors.New("the shell strategy can only copy from a container")
	}
	pod, remotePath := srcPod, srcPath
	if dstPod != "" {
		pod, remotePath = dstPod, dstPath
	}
	if remotePath == "" {
		return errors.New("remote path must not be empty")
	}

	config, err := o.flags.ToRESTConfig()
	if err != nil {
		return err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	ns, _, err := o.flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}
	if n, p, ok := strings.Cut(pod, "/"); ok {
		ns, pod = n, p
	}

	r, err := connect(ctx, o, &remote{client: client, config: config, ns: ns, pod: pod}, dstPod != "")
	if err != nil {
		return err
	}
	if srcPod != "" {
		return r.download(ctx, srcPath, dst)
	}
	return r.upload(ctx, src, dstPath)
}

// splitRemote splits [NAMESPACE/]POD:PATH, like kubectl cp does.
func splitRemote(arg string) (pod, path string) {
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, ".") {
		return "", arg
	}
	pod, path, ok := strings.Cut(arg, ":")
	if !ok {
		return "", arg
	}
	return pod, path
}
