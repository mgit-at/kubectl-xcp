#!/bin/sh
# Builds the release archives into dist/ and renders the krew manifest
# plugins/xcp.yaml for a tag, e.g. hack/release.sh v0.1.0.
set -eu
cd "$(dirname "$0")/.."

tag=${1:-}
case $tag in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
	echo "usage: $0 vMAJOR.MINOR.PATCH" >&2
	exit 1
	;;
esac
repo=https://github.com/mgit-at/kubectl-xcp
# Windows is missing: splitRemote takes C:\path for a pod named C.
platforms="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

rm -rf dist
mkdir dist
go generate ./...
for p in $platforms; do
	name=kubectl-xcp_${tag}_${p%/*}_${p#*/}
	mkdir "dist/$name"
	CGO_ENABLED=0 GOOS=${p%/*} GOARCH=${p#*/} go build -trimpath -ldflags "-s -w" -o "dist/$name/kubectl-xcp" .
	cp LICENSE "dist/$name/"
	tar -czf "dist/$name.tar.gz" -C "dist/$name" kubectl-xcp LICENSE
done
(cd dist && sha256sum -- *.tar.gz >checksums.txt)

mkdir -p plugins
{
	cat <<EOF
apiVersion: krew.googlecontainertools.github.com/v1alpha2
kind: Plugin
metadata:
  name: xcp
spec:
  version: $tag
  homepage: $repo
  shortDescription: Copy files to/from containers without tar
  description: |
    Copies files and directories to and from containers like kubectl cp, but
    also works when the container has no tar. Paths follow the rsync
    convention: dir/ copies the contents of dir, dir copies dir itself.

    Depending on what the container provides, it uses tar in the container,
    injects a small static tar helper, copies out file by file with sh and
    cat, adds an ephemeral container that reaches the container's filesystem
    through /proc/1/root, or as a last resort reads files with shell builtins.
  caveats: |
    Depending on the container, xcp may
    * leave a small tar helper in /tmp, /var/tmp, /dev/shm or an emptyDir
      mount of the container, for reuse by later runs,
    * add an ephemeral container to the pod, which Kubernetes cannot remove
      until the pod is deleted, and
    * as a last resort, copy out with shell builtins only, which corrupts
      binary files in containers without bash (with a warning).
    Pick a strategy explicitly with --strategy, see kubectl xcp --help.
  platforms:
EOF
	for p in $platforms; do
		name=kubectl-xcp_${tag}_${p%/*}_${p#*/}
		cat <<EOF
  - selector:
      matchLabels:
        os: ${p%/*}
        arch: ${p#*/}
    uri: $repo/releases/download/$tag/$name.tar.gz
    sha256: $(sha256sum "dist/$name.tar.gz" | cut -d' ' -f1)
    bin: kubectl-xcp
EOF
	done
} >plugins/xcp.yaml
