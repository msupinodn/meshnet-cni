package vxlan

import (
	"net"
	"testing"

	"github.com/networkop/meshnet-cni/internal/koko"
	"github.com/vishvananda/netlink"
)

func TestVxlan(t *testing.T) {
	tests := []struct {
		expected koko.VxLan
		found    *netlink.Vxlan
		same     bool
	}{
		{
			expected: koko.VxLan{
				ID:     5001,
				IPAddr: net.IPv4(1, 1, 1, 1),
			},
			found: &netlink.Vxlan{
				VxlanId: 5001,
				Group:   net.IPv4(1, 1, 1, 1),
			},
			same: true,
		},
		{
			expected: koko.VxLan{
				ID:     5001,
				IPAddr: net.IPv4(1, 1, 1, 1),
			},
			found: &netlink.Vxlan{
				VxlanId: 5002,
				Group:   net.IPv4(1, 1, 1, 1),
			},
			same: false,
		},
		{
			expected: koko.VxLan{
				ID:     5001,
				IPAddr: net.IPv4(1, 1, 1, 1),
			},
			found: &netlink.Vxlan{
				VxlanId: 5001,
				Group:   net.IPv4(2, 2, 2, 2),
			},
			same: false,
		},
	}
	for i, tt := range tests {
		result := vxlanDifferent(tt.found, tt.expected)
		if result != tt.same {
			t.Errorf("#%d test failed", i)
		}
	}
}
