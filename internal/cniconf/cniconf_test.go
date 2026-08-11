package cniconf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupDatapathIfacePrefix(t *testing.T) {
	orig := DatapathIfacePrefixConf
	t.Cleanup(func() { DatapathIfacePrefixConf = orig })

	t.Run("missing file is unset legacy up", func(t *testing.T) {
		DatapathIfacePrefixConf = filepath.Join(t.TempDir(), "missing")
		if prefix, ok := LookupDatapathIfacePrefix(); ok || prefix != "" {
			t.Fatalf("got (%q, %v), want (\"\", false)", prefix, ok)
		}
	})

	t.Run("eno prefix", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "eno")
		if err := os.WriteFile(path, []byte("eno"), 0o644); err != nil {
			t.Fatal(err)
		}
		DatapathIfacePrefixConf = path
		if prefix, ok := LookupDatapathIfacePrefix(); !ok || prefix != "eno" {
			t.Fatalf("got (%q, %v), want (eno, true)", prefix, ok)
		}
	})

	t.Run("eth prefix", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "eth")
		if err := os.WriteFile(path, []byte("eth"), 0o644); err != nil {
			t.Fatal(err)
		}
		DatapathIfacePrefixConf = path
		if prefix, ok := LookupDatapathIfacePrefix(); !ok || prefix != "eth" {
			t.Fatalf("got (%q, %v), want (eth, true)", prefix, ok)
		}
	})

	t.Run("empty file means all interfaces down", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(path, []byte("  \n"), 0o644); err != nil {
			t.Fatal(err)
		}
		DatapathIfacePrefixConf = path
		if prefix, ok := LookupDatapathIfacePrefix(); !ok || prefix != "" {
			t.Fatalf("got (%q, %v), want (\"\", true)", prefix, ok)
		}
	})
}

func TestLookupInterNodeLinkType(t *testing.T) {
	orig := InterNodeLinkTypeConf
	t.Cleanup(func() { InterNodeLinkTypeConf = orig })

	t.Run("missing file defaults to GRPC", func(t *testing.T) {
		InterNodeLinkTypeConf = filepath.Join(t.TempDir(), "missing")
		if got := LookupInterNodeLinkType(); got != "GRPC" {
			t.Fatalf("got %q, want GRPC", got)
		}
	})

	t.Run("VXLAN file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vxlan")
		if err := os.WriteFile(path, []byte("VXLAN"), 0o644); err != nil {
			t.Fatal(err)
		}
		InterNodeLinkTypeConf = path
		if got := LookupInterNodeLinkType(); got != "VXLAN" {
			t.Fatalf("got %q, want VXLAN", got)
		}
	})
}
