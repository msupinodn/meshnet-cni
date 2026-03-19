package grpcwire

import (
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
