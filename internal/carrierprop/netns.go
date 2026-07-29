package carrierprop

import (
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// OperUpOnHost reads link oper-state in the host network namespace.
func OperUpOnHost(ifindex int) (bool, error) {
	link, err := netlink.LinkByIndex(ifindex)
	if err != nil {
		return false, err
	}
	return OperUp(link), nil
}

// OperUpOnHostByName reads link oper-state in the host network namespace.
func OperUpOnHostByName(name string) (bool, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return false, err
	}
	return OperUp(link), nil
}

// OperUpInPodNetNS reads link oper-state inside the pod network namespace.
// netlink.LinkByName uses a process-wide handle bound to the host netns, so
// callers must open a per-namespace handle after setns (inside ns.Do).
func OperUpInPodNetNS(netNSPath, ifaceName string) (bool, error) {
	podNs, err := ns.GetNS(netNSPath)
	if err != nil {
		return false, err
	}
	defer podNs.Close()

	var oper bool
	err = podNs.Do(func(_ ns.NetNS) error {
		h, err := netlink.NewHandle()
		if err != nil {
			return err
		}

		link, err := h.LinkByName(ifaceName)
		if err != nil {
			return err
		}
		oper = OperUp(link)
		return nil
	})
	return oper, err
}
