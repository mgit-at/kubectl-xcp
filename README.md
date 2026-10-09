# kubectl-xcp

`kubectl cp` that also works when the container has no `tar`, and for people
who are not cluster-admin.

`kubectl cp` needs `tar` inside the container, which minimal, distroless and
hardened images often lack. `kubectl xcp` tries every way of getting files in
and out that the container and your RBAC permissions allow, from the least to
the most invasive, and tells you which one it used. In doing so it is also a
reminder of how intricate Kubernetes security settings are: it uses every gap
they leave.

- Copies files and whole directories, in both directions.
- Paths follow the rsync convention: `dir/` copies the contents of `dir`,
  `dir` copies the directory itself.
- Files coming from the container are treated as untrusted.

## Install

With [krew](https://krew.sigs.k8s.io/), from the custom index in this repo:

```sh
kubectl krew index add mgit https://github.com/mgit-at/kubectl-xcp.git
kubectl krew install mgit/xcp
```

Or download an archive from the [releases](https://github.com/mgit-at/kubectl-xcp/releases)
(Linux and macOS, amd64 and arm64) and put `kubectl-xcp` into your `$PATH`.

## Usage

```sh
kubectl xcp [flags] SRC DST
```

One of `SRC` and `DST` is a remote path `[NAMESPACE/]POD:PATH`, the other a
local path.

```sh
kubectl xcp ./conf/ mypod:/etc/app/          # contents of ./conf into /etc/app
kubectl xcp mypod:/data ./backup             # creates ./backup/data
kubectl xcp mypod:/var/log/app.log app.log   # single file under a new name
kubectl xcp -c app ns/mypod:/etc/app.yaml .  # container app in namespace ns
```

| SRC | DST | Result |
|---|---|---|
| `dir/` | `d` | contents of `dir` in `d/`, `d` is created if missing |
| `dir` | `d` | `d/dir/…` |
| `file` | `d/` or an existing directory `d` | `d/file`, `d/` is created if missing |
| `file` | `d` (no directory) | the file is written as `d` |

Flags besides the usual kubectl ones (`-n`, `--context`, `--kubeconfig`, …):

| Flag | Meaning |
|---|---|
| `-c, --container` | container, defaults to the pod's default container |
| `--strategy` | `auto` (default), or one of the strategies below |
| `--image` | image for the ephemeral container, must contain `tar` and `sh` (default `mirror.gcr.io/library/busybox:1.37`, a Docker Hub mirror without pull limits) |
| `--uid`, `--gid` | user and group for the ephemeral container |

## How it works

All strategies stream a tar archive over `pods/exec`, the same channel
`kubectl cp` and `kubectl exec` use. With `--strategy auto`, xcp tries them in
this order and prints a note when it falls back:

| Strategy | Direction | Needs in the container | Needs in RBAC | Caveats |
|---|---|---|---|---|
| `exec` | both | `tar` or `busybox tar` | `pods/exec` | none, like `kubectl cp` |
| `inject` | both | `sh`, `cat`, `chmod`, `uname`, a writable and executable directory | `pods/exec` | leaves a 2 MB helper in the container |
| `shell` | from the container | `sh`, `cat` | `pods/exec` | slow for many files, modes approximated, times not preserved |
| `ephemeral` | both | nothing | `pods/exec`, `patch` on `pods/ephemeralcontainers` | changes the pod spec for good |
| `shell-builtins` | from the container | `bash` or `sh` | `pods/exec` | **with plain `sh`, binary files are corrupted**; slow |

- **inject** copies a small static tar of its own (x86_64 or aarch64) into
  `/tmp`, `/var/tmp`, `/dev/shm` or an `emptyDir` mount of the container,
  whichever is writable and allows running programs. It stays there as
  `.kubectl-xcp-<hash>` and is reused by later runs.
- **shell** lists the files with `sh` and fetches each one with its own `cat`,
  so it works on read-only root filesystems. Symlinks need `readlink` and are
  skipped without it.
- **ephemeral** adds an ephemeral container with `tar` that shares the target's
  process namespace and reaches its filesystem, including volumes, through
  `/proc/1/root`.
  - Kubernetes cannot remove ephemeral containers until the pod is deleted.
    xcp reuses a running one from an earlier run.
  - It copies the target's security context, as the kernel only allows access
    with the same user and capabilities. If the target's user only comes from
    its image, pass the matching `--uid`/`--gid`.
  - It does not work for pods with `shareProcessNamespace: true`, and absolute
    symlinks in the remote path resolve inside the ephemeral container.
- **shell-builtins** is the last resort when there is not even `cat` and no
  ephemeral container can be added. It reads files with shell builtins only and
  prints a warning with these caveats:
  - With `bash`, files arrive intact.
  - With a POSIX `sh` such as dash or busybox, NUL bytes are silently dropped:
    text files arrive intact, binary files are corrupted.

Without any shell and without ephemeral containers, nothing is left to copy
with.

## Security

- Archives from the container are unpacked through Go's `os.Root`: entries
  escaping the destination (`..`, absolute paths, symlinks) are rejected.
- When unpacking locally, setuid and similar bits are not restored, and hard
  links and special files are skipped with a warning.
- Paths are always passed to the container as arguments, never pasted into
  shell commands.

## Limitations

- No Windows build: local Windows paths like `C:\dir` would be taken for a pod
  named `C`.
- Untested: skipping `noexec` directories in `inject`, and the aarch64 helper.

## Development

```sh
go generate                                         # builds the embedded helpers into helper/bin
go build -trimpath -ldflags "-s -w" -o kubectl-xcp .
go test ./...                                       # unit tests, no cluster needed
./hack/e2e.sh                                       # e2e tests in a throwaway kind cluster
```

The e2e tests need kind, docker and kubectl. They refuse to run against
anything but a `kind-*` context, as they add ephemeral containers that cannot
be removed again. Both test suites run on pushes to `main` and on pull requests.

A release is made by pushing a `vMAJOR.MINOR.PATCH` tag: the release workflow
runs the tests, publishes the archives and commits the krew manifest
`plugins/xcp.yaml` to `main`.

## Ideas

- Copying from containers without any shell where ephemeral containers are not
  allowed, e.g. with a helper pod mounting the same volume.
- Debugging *inside* a running container: a shell or a simple editor like `vi`
  injected into it, with all its processes, files and volumes at hand, which
  `kubectl debug` does not offer.

## Mentions

[kubectl-superdebug](https://github.com/JonMerlevede/kubectl-superdebug): a
really nice tool, though it mostly needs permissions to patch pods.
