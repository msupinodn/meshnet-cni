package carrierprop

import (
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

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
