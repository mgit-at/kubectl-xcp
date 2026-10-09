#!/bin/sh
# Runs the e2e tests against a throwaway kind cluster, which is deleted
# afterwards. Extra arguments are passed to go test, e.g. -run TestCopy.
set -eu
cd "$(dirname "$0")/.."

name=xcp-e2e
kubeconfig=$(mktemp)
trap 'kind delete cluster --name "$name" --kubeconfig "$kubeconfig"; rm -f "$kubeconfig"' EXIT

go generate ./...
docker build -q -t xcp-e2e-notar:test e2e/testdata/notar
docker build -q -t xcp-e2e-nouname:test e2e/testdata/nouname
# Images come from mirror.gcr.io, Docker Hub rate-limits the shared CI runners.
kind create cluster --name "$name" --kubeconfig "$kubeconfig" --image mirror.gcr.io/kindest/node:v1.37.0
kind load docker-image --name "$name" xcp-e2e-notar:test xcp-e2e-nouname:test

XCP_E2E_KUBECONFIG=$kubeconfig go test -tags e2e -count=1 -v ./e2e "$@"
