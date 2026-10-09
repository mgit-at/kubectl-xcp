package main

import (
	"context"
	"strings"
	"testing"
)

func TestSplitRemote(t *testing.T) {
	for _, tc := range []struct{ arg, pod, path string }{
		{"pod:/data", "pod", "/data"},
		{"ns/pod:dir/", "ns/pod", "dir/"},
		{"pod:", "pod", ""},
		{"local", "", "local"},
		{"/abs/with:colon", "", "/abs/with:colon"},
		{"./rel:colon", "", "./rel:colon"},
	} {
		pod, path := splitRemote(tc.arg)
		if pod != tc.pod || path != tc.path {
			t.Errorf("splitRemote(%q) = %q, %q, want %q, %q", tc.arg, pod, path, tc.pod, tc.path)
		}
	}
}

// These are rejected before any kubeconfig is loaded.
func TestRunRejectsArguments(t *testing.T) {
	for _, tc := range []struct{ strategy, src, dst, err string }{
		{"foo", "pod:/a", "b", "unknown strategy"},
		{"auto", "a", "b", "exactly one"},
		{"auto", "pod:/a", "pod:/b", "exactly one"},
		{"auto", "pod:", "b", "must not be empty"},
		{"shell", "a", "pod:/b", "only copy from a container"},
		{"shell-builtins", "a", "pod:/b", "only copy from a container"},
	} {
		err := run(context.Background(), &options{strategy: tc.strategy}, tc.src, tc.dst)
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("run(%q, %q, %q) = %v, want %q", tc.strategy, tc.src, tc.dst, err, tc.err)
		}
	}
}
