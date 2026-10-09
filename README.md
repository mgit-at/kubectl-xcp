# kubectl-xcp

Make every debugging way that is possible, easy to use!

This project should also serve as a stark reminder of the complexity of kubernetes security mechanism, because this tool will use every method to reach it's goal even if it's a results of missunderstood or hard to use kubernetes security configs.

And will hopefully one day provided via a simple install with [krew - kubectl plugin manager](https://krew.sigs.k8s.io/) 

## Usage

```sh
go generate && go build -trimpath -ldflags "-s -w" -o kubectl-xcp . && mv kubectl-xcp ~/.local/bin/   # anywhere in $PATH

kubectl xcp ./conf/ mypod:/etc/app/      # contents of conf into /etc/app
kubectl xcp mypod:/data ./backup         # creates ./backup/data
kubectl xcp -c app ns/mypod:/etc/app.yaml app.yaml
```

Paths follow the rsync convention on both sides: `dir/` copies the contents of
`dir`, `dir` copies the directory itself, and a destination ending in `/` is
always a directory (created if missing).

How the copy is done (`--strategy`, default `auto` tries them in order):

1. `exec`: `tar` (or `busybox tar`) inside the container, like `kubectl cp`.
   Needs `pods/exec`.
2. `inject`: copies a small static tar helper (`helper/`, embedded for x86_64
   and aarch64) into the container and runs it. Needs `pods/exec` and `sh`,
   `cat`, `chmod` and `uname` in the container, plus a writable, executable
   directory among `/tmp`, `/var/tmp`, `/dev/shm` and the container's
   `emptyDir` mounts. The helper stays there as `.kubectl-xcp-<hash>` and is
   reused by later runs.
3. `shell`: only for copying from the container. Lists the files with `sh`
   and fetches each with its own `cat` (`readlink` for symlinks), so it works
   on read-only root filesystems. Modes are approximated (executable or not)
   and times are not preserved; slow for many small files.
4. `ephemeral`: adds an ephemeral container (`--image`, default `busybox:1.37`)
   targeting the container and reaches its filesystem, including volumes,
   through `/proc/1/root`. Needs `pods/exec` and `patch` on
   `pods/ephemeralcontainers`, like `kubectl debug`.
   - The ephemeral container copies the target's security context. If the
     target's user only comes from its image, pass the matching `--uid`/`--gid`.
   - Ephemeral containers cannot be removed; a running matching one is reused.
   - Does not work for pods with `shareProcessNamespace: true`, and absolute
     symlinks in the remote path resolve inside the ephemeral container.

Files from the container are treated as untrusted: entries escaping the
destination (`..`, absolute paths, symlinks) are rejected. Hard links and
special files are skipped with a warning.

## GOALs

working tooling for debgging container in k8s in environments that don't have an "allow all" config or where the debugging person is NOT cluster-admin

1. working `kubectl cp` without `tar` inside container
2. working `kubectl debug` that injects intself INTO the running container (thus all processes and all files and PVs are there)
3. Having a simple editor like `vi` ready to just change some config files on an in use PV

## TODOs

- `kubectl cp` for containers without a shell where ephemeral containers are not allowed (e.g. a helper pod mounting the same PVC)
- usable `kubectl debug` to connect INTO a running container in a pod and debug running daemons
  - like interacting with files and PVs (this does not work with `kubectl debug` and makes it practically useless for most of our usecases)
  - seeing processes (granted that works with `kubectl debug --target`)
 
## Mentions

[kubectl-superdebug](https://github.com/JonMerlevede/kubectl-superdebug) - really nice tool, though mostly lacking permissions to patch pods
