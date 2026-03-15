// Package koko provides network link management functionality for creating
// veth pairs, vxlan tunnels, and macvlan interfaces inside container namespaces.
// Derived from github.com/redhat-nfvpe/koko (unmaintained), trimmed to only
// the subset used by meshnet-cni.
package koko

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/containernetworking/plugins/pkg/utils/sysctl"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

// VEth describes a veth endpoint inside (or outside) a network namespace.
type VEth struct {
	NsName   string      // network namespace path (empty = current ns)
	LinkName string      // interface name
	IPAddr   []net.IPNet // optional IPv4/v6 addresses
}

// VxLan describes a VXLAN tunnel endpoint.
type VxLan struct {
	ParentIF string // parent interface name
	ID       int    // VXLAN Network Identifier
	IPAddr   net.IP // remote VTEP address
	MTU      int    // optional MTU override
	UDPPort  int    // optional UDP port (default 4789)
}

// MacVLan describes a macvlan endpoint.
type MacVLan struct {
	ParentIF string              // parent interface name
	Mode     netlink.MacvlanMode // macvlan mode
}

func cleanupLink(link netlink.Link) {
	if err := netlink.LinkDel(link); err != nil {
		log.Warnf("koko: cleanup: failed to delete link: %v", err)
	}
}

func getRandomIFName() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("failed to read random bytes: %v", err))
	}
	return fmt.Sprintf("koko%d", binary.LittleEndian.Uint32(b[:]))
}

func makeVethPair(name, peer string, mtu int) (netlink.Link, error) {
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name:  name,
			Flags: net.FlagUp,
			MTU:   mtu,
		},
		PeerName: peer,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return nil, err
	}
	return veth, nil
}

// GetVethPair creates a veth pair and returns both links.
func GetVethPair(name1, name2 string) (link1, link2 netlink.Link, err error) {
	link1, err = makeVethPair(name1, name2, 1500)
	if err != nil {
		if os.IsExist(err) {
			err = fmt.Errorf("container veth name provided (%v) already exists", name1)
		} else {
			err = fmt.Errorf("failed to make veth pair: %v", err)
		}
		return
	}
	if link2, err = netlink.LinkByName(name2); err != nil {
		err = fmt.Errorf("failed to lookup %q: %v", name2, err)
	}
	return
}

// AddVxLanInterface creates a VXLAN interface on the host.
func AddVxLanInterface(vxlan VxLan, devName string) error {
	var parentIF netlink.Link
	var err error
	udpPort := 4789

	if parentIF, err = netlink.LinkByName(vxlan.ParentIF); err != nil {
		return fmt.Errorf("failed to get %s: %v", vxlan.ParentIF, err)
	}
	if vxlan.UDPPort != 0 {
		udpPort = vxlan.UDPPort
	}

	vxlanconf := netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:   devName,
			TxQLen: 1000,
		},
		VxlanId:      vxlan.ID,
		VtepDevIndex: parentIF.Attrs().Index,
		Group:        vxlan.IPAddr,
		Port:         udpPort,
		Learning:     true,
		L2miss:       true,
		L3miss:       true,
	}
	if vxlan.MTU != 0 {
		vxlanconf.LinkAttrs.MTU = vxlan.MTU
	}
	if err = netlink.LinkAdd(&vxlanconf); err != nil {
		return fmt.Errorf("failed to add vxlan %s: %v", devName, err)
	}
	return nil
}

// AddMacVLanInterface creates a macvlan interface on the host.
func AddMacVLanInterface(macvlan MacVLan, devName string) error {
	var parentIF netlink.Link
	var err error

	if parentIF, err = netlink.LinkByName(macvlan.ParentIF); err != nil {
		return fmt.Errorf("failed to get %s: %v", macvlan.ParentIF, err)
	}

	macvlanconf := netlink.Macvlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        devName,
			ParentIndex: parentIF.Attrs().Index,
		},
		Mode: macvlan.Mode,
	}
	if err = netlink.LinkAdd(&macvlanconf); err != nil {
		return fmt.Errorf("failed to add macvlan %s: %v", devName, err)
	}
	return nil
}

// SetVethLink moves a link into the VEth's namespace, renames it, sets it up,
// and optionally assigns IP addresses.
func (veth *VEth) SetVethLink(link netlink.Link) error {
	var vethNs ns.NetNS
	var err error

	vethLinkName := link.Attrs().Name
	if veth.NsName == "" {
		vethNs, err = ns.GetCurrentNS()
	} else {
		vethNs, err = ns.GetNS(veth.NsName)
	}
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	defer vethNs.Close()

	fd := vethNs.Fd()
	maxInt := uintptr(int(^uint(0) >> 1))
	if fd > maxInt {
		return fmt.Errorf("namespace file descriptor %d overflows int", fd)
	}
	if err = netlink.LinkSetNsFd(link, int(fd)); err != nil { // #nosec G115
		return fmt.Errorf("%v", err)
	}

	return vethNs.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(vethLinkName)
		if err != nil {
			return fmt.Errorf("failed to lookup %q in %q: %v",
				veth.LinkName, vethNs.Path(), err)
		}

		if veth.LinkName != vethLinkName {
			if err = netlink.LinkSetName(link, veth.LinkName); err != nil {
				return fmt.Errorf("failed to rename link %s -> %s: %v",
					vethLinkName, veth.LinkName, err)
			}
		}

		if err = netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("failed to set %q up: %v", veth.LinkName, err)
		}

		for i := 0; i < len(veth.IPAddr); i++ {
			if veth.IPAddr[i].IP.To4() == nil {
				ipv6SysctlName := fmt.Sprintf("net.ipv6.conf.%s.disable_ipv6", veth.LinkName)
				if _, err := sysctl.Sysctl(ipv6SysctlName, "0"); err != nil {
					return fmt.Errorf("failed to set ipv6.disable to 0 at %s: %v",
						veth.LinkName, err)
				}
			}
			addr := &netlink.Addr{IPNet: &veth.IPAddr[i], Label: ""}
			if err = netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add IP addr %v to %q: %v",
					addr, veth.LinkName, err)
			}
		}
		return nil
	})
}

// RemoveVethLink removes the link from its namespace.
func (veth *VEth) RemoveVethLink() error {
	var vethNs ns.NetNS
	var err error

	if veth.NsName == "" {
		vethNs, err = ns.GetCurrentNS()
	} else {
		vethNs, err = ns.GetNS(veth.NsName)
	}
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	defer vethNs.Close()

	return vethNs.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(veth.LinkName)
		if err != nil {
			return fmt.Errorf("failed to lookup %q in %q: %v",
				veth.LinkName, vethNs.Path(), err)
		}
		if err = netlink.LinkDel(link); err != nil {
			return fmt.Errorf("failed to remove link %q in %q: %v",
				veth.LinkName, vethNs.Path(), err)
		}
		return nil
	})
}

// MakeVeth creates a veth pair and places each end into the respective namespace.
func MakeVeth(veth1, veth2 VEth) error {
	tempLinkName1 := veth1.LinkName
	tempLinkName2 := veth2.LinkName

	if veth1.NsName != "" {
		tempLinkName1 = getRandomIFName()
	}
	if veth2.NsName != "" {
		tempLinkName2 = getRandomIFName()
	}

	link1, link2, err := GetVethPair(tempLinkName1, tempLinkName2)
	if err != nil {
		return err
	}

	if err = veth1.SetVethLink(link1); err != nil {
		cleanupLink(link1)
		return err
	}
	if err = veth2.SetVethLink(link2); err != nil {
		cleanupLink(link2)
	}
	return err
}

// MakeVxLan creates a VXLAN interface and moves it into the container namespace.
func MakeVxLan(veth1 VEth, vxlan VxLan) error {
	tempLinkName := getRandomIFName()

	if err := AddVxLanInterface(vxlan, tempLinkName); err != nil {
		return fmt.Errorf("vxlan add failed: %v", err)
	}

	link, err := netlink.LinkByName(tempLinkName)
	if err != nil {
		return fmt.Errorf("cannot get %s: %v", tempLinkName, err)
	}

	if err = veth1.SetVethLink(link); err != nil {
		cleanupLink(link)
		return fmt.Errorf("cannot add IPaddr/netns: %v", err)
	}
	return nil
}

// MakeMacVLan creates a macvlan interface and configures it.
func MakeMacVLan(veth1 VEth, macvlan MacVLan) error {
	if err := AddMacVLanInterface(macvlan, veth1.LinkName); err != nil {
		return fmt.Errorf("macvlan add failed: %v", err)
	}

	link, err := netlink.LinkByName(veth1.LinkName)
	if err != nil {
		return fmt.Errorf("cannot get %s: %v", veth1.LinkName, err)
	}

	if err = veth1.SetVethLink(link); err != nil {
		return fmt.Errorf("cannot add IPaddr/netns: %v", err)
	}
	return nil
}

// IsExistLinkInNS checks if an interface exists in the given namespace.
func IsExistLinkInNS(nsName, linkName string) (bool, error) {
	var vethNs ns.NetNS
	var err error

	if nsName == "" {
		vethNs, err = ns.GetCurrentNS()
	} else {
		vethNs, err = ns.GetNS(nsName)
	}
	if err != nil {
		return false, err
	}
	defer vethNs.Close()

	var found bool
	err = vethNs.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(linkName)
		if err != nil {
			return err
		}
		found = link != nil
		return nil
	})
	return found, err
}
