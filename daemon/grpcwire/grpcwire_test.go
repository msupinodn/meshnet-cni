package grpcwire

import (
	"fmt"
	"testing"
)

const maxIfnamsiz = 15 // IFNAMSIZ-1, max user-visible interface name length

func TestGenNodeIfaceName_IFNAMSIZ(t *testing.T) {
	tests := []struct {
		name         string
		podName      string
		podIfaceName string
	}{
		{"short names", "pod1", "eno0"},
		{"max prefix length", "abcde", "fghij"},
		{"long pod name truncated", "my-very-long-pod-name-that-exceeds-everything", "eno0"},
		{"long intf name truncated", "pod1", "interface-name-very-long"},
		{"both long", "reallylong-dal01-extra-characters", "eno99-and-more-chars"},
		{"exactly 5 char names", "abcde", "eno99"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, err := GenNodeIfaceName(tt.podName, tt.podIfaceName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(name) > maxIfnamsiz {
				t.Errorf("interface name %q is %d chars, exceeds IFNAMSIZ (%d)",
					name, len(name), maxIfnamsiz)
			}
		})
	}
}

func TestGenNodeIfaceName_HighIndex(t *testing.T) {
	// Simulate the counter being very high (past the 10000 boundary)
	indexGen.mu.Lock()
	indexGen.currId = 99999
	indexGen.mu.Unlock()

	name, err := GenNodeIfaceName("reallylong-dal01", "eno99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(name) > maxIfnamsiz {
		t.Errorf("interface name %q is %d chars, exceeds IFNAMSIZ (%d) at high index",
			name, len(name), maxIfnamsiz)
	}
}

func TestGenNodeIfaceName_WrapAround(t *testing.T) {
	// Verify index wraps and produces 4-digit suffix
	indexGen.mu.Lock()
	indexGen.currId = 9999
	indexGen.mu.Unlock()

	name1, _ := GenNodeIfaceName("podAB", "eno0")

	// Next call wraps to 0
	name2, _ := GenNodeIfaceName("podAB", "eno0")

	if name1 == name2 {
		t.Errorf("expected different names after wrap, both got %q", name1)
	}
	if len(name1) > maxIfnamsiz || len(name2) > maxIfnamsiz {
		t.Errorf("names exceed IFNAMSIZ: %q (%d), %q (%d)",
			name1, len(name1), name2, len(name2))
	}
}

func TestSeedIndexFromHost(t *testing.T) {
	// SeedIndexFromHost reads real host interfaces. After calling it the
	// counter must be at least as high as any -NNNN suffix found on the
	// host. We can't control which interfaces exist, but we can verify
	// the function doesn't panic and that subsequent names are unique
	// and within IFNAMSIZ.
	indexGen.mu.Lock()
	indexGen.currId = 0
	indexGen.mu.Unlock()

	SeedIndexFromHost()

	indexGen.mu.Lock()
	seeded := indexGen.currId
	indexGen.mu.Unlock()

	// Generate two names and confirm they don't collide and are valid.
	name1, err := GenNodeIfaceName("seedt", "eth0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	name2, err := GenNodeIfaceName("seedt", "eth0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name1 == name2 {
		t.Errorf("names should differ after seed, both got %q (seeded at %d)", name1, seeded)
	}
	if len(name1) > maxIfnamsiz || len(name2) > maxIfnamsiz {
		t.Errorf("names exceed IFNAMSIZ: %q (%d), %q (%d)",
			name1, len(name1), name2, len(name2))
	}
}

func TestIfaceSuffixRe(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantID  int64
		wantHit bool
	}{
		{"meshnet iface", "pod1beth1-0042", 42, true},
		{"high index", "abcdefghij-9999", 9999, true},
		{"no suffix", "eth0", 0, false},
		{"short suffix", "foo-01", 0, false},
		{"loopback", "lo", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ifaceSuffixRe.FindStringSubmatch(tt.input)
			if tt.wantHit {
				if m == nil {
					t.Fatalf("expected match for %q", tt.input)
				}
				// m[1] is the 4-digit group
				if m[1] != fmt.Sprintf("%04d", tt.wantID) {
					t.Errorf("got suffix %q, want %04d", m[1], tt.wantID)
				}
			} else {
				if m != nil {
					t.Errorf("expected no match for %q, got %v", tt.input, m)
				}
			}
		})
	}
}
