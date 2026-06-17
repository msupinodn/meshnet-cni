package cni

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// baseConflist is a minimal valid base CNI conflist that meshnet chains onto.
const baseConflist = `{
	"cniVersion": "0.3.1",
	"name": "azure",
	"plugins": [{"type": "azure-vnet"}]
}`

func withTempNetDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := defaultNetDir
	defaultNetDir = dir
	t.Cleanup(func() { defaultNetDir = orig })
	return dir
}

// WaitForNetConfig should return promptly once a valid base config is present.
func TestWaitForNetConfig_AlreadyPresent(t *testing.T) {
	dir := withTempNetDir(t)
	if err := os.WriteFile(filepath.Join(dir, "10-azure.conflist"), []byte(baseConflist), 0o644); err != nil {
		t.Fatalf("write conflist: %v", err)
	}

	if err := WaitForNetConfig(time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("WaitForNetConfig with present config returned error: %v", err)
	}
}

// WaitForNetConfig should keep polling and succeed once the base config appears,
// mirroring a freshly-joined node where the conflist is written shortly after
// meshnetd starts.
func TestWaitForNetConfig_AppearsLater(t *testing.T) {
	dir := withTempNetDir(t)

	done := make(chan error, 1)
	go func() {
		done <- WaitForNetConfig(2*time.Second, 10*time.Millisecond)
	}()

	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "10-azure.conflist"), []byte(baseConflist), 0o644); err != nil {
		t.Fatalf("write conflist: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForNetConfig returned error after config appeared: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForNetConfig did not return after config appeared")
	}
}

// With an empty net dir WaitForNetConfig should time out and surface an error so
// the caller can treat it as a genuine misconfiguration.
func TestWaitForNetConfig_Timeout(t *testing.T) {
	withTempNetDir(t)

	err := WaitForNetConfig(60*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error with empty net dir, got nil")
	}
}

// A net dir containing only meshnet's own config must not satisfy the wait: the
// base config meshnet chains onto is still missing.
func TestWaitForNetConfig_OnlyMeshnetIgnored(t *testing.T) {
	dir := withTempNetDir(t)
	if err := os.WriteFile(filepath.Join(dir, defaultCNIFile), []byte(baseConflist), 0o644); err != nil {
		t.Fatalf("write meshnet conflist: %v", err)
	}

	err := WaitForNetConfig(60*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout when only meshnet config present, got nil")
	}
}
