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
