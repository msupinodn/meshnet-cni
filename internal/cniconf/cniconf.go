// Package cniconf holds host paths the meshnet daemon writes for the CNI plugin.
package cniconf

import (
	"os"
	"strconv"
	"strings"

	"github.com/networkop/meshnet-cni/utils/wireutil"
)

// DatapathIfacePrefixConf is written by meshnetd when MESHNET_DATAPATH_IFACE_PREFIX is set.
var DatapathIfacePrefixConf = "/etc/cni/net.d/meshnet-datapath-iface-prefix"

// DefaultLinkMTUConf is written by meshnetd when MESHNET_DEFAULT_LINK_MTU is set.
var DefaultLinkMTUConf = "/etc/cni/net.d/meshnet-default-link-mtu"

// InterNodeLinkTypeConf is written by meshnetd from INTER_NODE_LINK_TYPE.
// The CNI plugin reads this file because kubelet invokes it without the daemon env.
var InterNodeLinkTypeConf = "/etc/cni/net.d/meshnet-inter-node-link-type"

// LookupDatapathIfacePrefix reports whether default admin-down is configured.
//
//   - ok=false (env unset / file missing): legacy behaviour — all interfaces up after CNI ADD.
//   - ok=true, prefix="": leave every interface admin-down.
//   - ok=true, prefix="eno": leave eno<N> down; prefix="eth" leaves eth<N> down; etc.
func LookupDatapathIfacePrefix() (prefix string, ok bool) {
	b, err := os.ReadFile(DatapathIfacePrefixConf)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// ReadDefaultLinkMTU returns the default pod link MTU when topology omits mtu.
func ReadDefaultLinkMTU() (int, bool) {
	b, err := os.ReadFile(DefaultLinkMTUConf)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// LookupInterNodeLinkType returns GRPC or VXLAN from the host file written by meshnetd.
func LookupInterNodeLinkType() string {
	b, err := os.ReadFile(InterNodeLinkTypeConf)
	if err != nil {
		return wireutil.INTER_NODE_LINK_GRPC
	}
	return wireutil.ResolveInterNodeLinkType(string(b))
}
