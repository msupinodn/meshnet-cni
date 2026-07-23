package koko

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/networkop/meshnet-cni/internal/cniconf"
)

func TestIsDatapathIface(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   bool
	}{
		{"eno1", "eno", true},
		{"eno99", "eno", true},
		{"eno0", "eno", true},
		{"eno", "eno", false},
		{"enolink", "eno", false},
		{"eth0", "eno", false},
		{"dnos1eno1-0001", "eno", false},
		{"eth1", "eth", true},
		{"eth0", "eth", true},
		{"eth", "eth", false},
		{"ethernet", "eth", false},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.prefix, func(t *testing.T) {
			if got := isDatapathIface(tt.name, tt.prefix); got != tt.want {
				t.Fatalf("isDatapathIface(%q, %q) = %v, want %v", tt.name, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestShouldLeaveAdminDown(t *testing.T) {
	orig := cniconf.DatapathIfacePrefixConf
	t.Cleanup(func() { cniconf.DatapathIfacePrefixConf = orig })

	t.Run("unset brings all up", func(t *testing.T) {
		cniconf.DatapathIfacePrefixConf = filepath.Join(t.TempDir(), "missing")
		if shouldLeaveAdminDown("eno1") {
			t.Fatal("expected eno1 up when env unset")
		}
	})

	t.Run("empty prefix leaves all down", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "all")
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		cniconf.DatapathIfacePrefixConf = path
		for _, name := range []string{"eno1", "eth0", "mgmt0"} {
			if !shouldLeaveAdminDown(name) {
				t.Fatalf("expected %s down when prefix is empty", name)
			}
		}
	})

	t.Run("eno prefix", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "eno")
		if err := os.WriteFile(path, []byte("eno"), 0o644); err != nil {
			t.Fatal(err)
		}
		cniconf.DatapathIfacePrefixConf = path
		if !shouldLeaveAdminDown("eno1") || shouldLeaveAdminDown("eth0") {
			t.Fatal("expected only eno<N> down")
		}
	})

	t.Run("eth prefix", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "eth")
		if err := os.WriteFile(path, []byte("eth"), 0o644); err != nil {
			t.Fatal(err)
		}
		cniconf.DatapathIfacePrefixConf = path
		if !shouldLeaveAdminDown("eth1") || shouldLeaveAdminDown("eno1") {
			t.Fatal("expected only eth<N> down")
		}
	})
}
