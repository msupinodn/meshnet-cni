package wireutil

import "testing"

func TestResolveInterNodeLinkType(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", INTER_NODE_LINK_GRPC},
		{"GRPC", INTER_NODE_LINK_GRPC},
		{" VXLAN ", INTER_NODE_LINK_VXLAN},
		{"bogus", INTER_NODE_LINK_GRPC},
	}
	for _, tc := range tests {
		if got := ResolveInterNodeLinkType(tc.in); got != tc.want {
			t.Fatalf("ResolveInterNodeLinkType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
