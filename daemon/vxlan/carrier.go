package vxlan

import (
	"fmt"
	"time"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/networkop/meshnet-cni/internal/carrierprop"
	"github.com/vishvananda/netlink"

	mpb "github.com/networkop/meshnet-cni/daemon/proto/meshnet/v1beta1"
)

// StartCarrierWatch polls carrier on tracked in-pod VXLAN/datapath interfaces
// and notifies the peer node on debounced transitions. No-op unless carrier
// propagation is enabled.
func StartCarrierWatch(stopC <-chan struct{}) {
	if !carrierprop.Enabled() {
		return
	}
	vxLanOvrlyLogger.Infof("StartCarrierWatch: monitoring in-pod VXLAN interface carrier")

	deb := carrierprop.NewDebouncer(carrierprop.Debounce)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stopC:
			return
		case now := <-ticker.C:
			for _, link := range snapshotLinks() {
				up, err := linkOperUp(link)
				if err != nil {
					continue
				}
				deb.Observe(int64(link.UID), up, now)
			}
			for _, e := range deb.Due(now) {
				if carrierprop.ConsumeEcho(e.Key, e.Up) {
					continue
				}
				propagateEdge(int(e.Key), e.Up)
			}
		}
	}
}

func linkOperUp(link *Link) (bool, error) {
	vethNs, err := ns.GetNS(link.LocalNetNS)
	if err != nil {
		return false, err
	}
	defer vethNs.Close()

	var oper bool
	err = vethNs.Do(func(_ ns.NetNS) error {
		l, err := netlink.LinkByName(link.LocalIntfName)
		if err != nil {
			return err
		}
		oper = carrierprop.OperUp(l)
		return nil
	})
	return oper, err
}

func propagateEdge(linkUID int, up bool) {
	link, ok := GetLinkByUID(linkUID)
	if !ok {
		return
	}
	vxLanOvrlyLogger.Infof("carrier: local vxlan %s (uid %d) went %s; notifying peer %s",
		link.LocalIntfName, link.UID, carrierprop.UpStr(up), link.PeerNodeIP)
	if err := carrierprop.NotifyPeerLinkState(link.PeerNodeIP, &mpb.WireLinkState{
		LinkUid: int64(link.UID),
		Up:      up,
	}); err != nil {
		vxLanOvrlyLogger.Errorf("carrier: failed to notify peer %s for uid %d: %v", link.PeerNodeIP, link.UID, err)
	}
}

// SetLocalLinkState brings the peer's in-pod VXLAN/datapath interface up or down.
func SetLocalLinkState(linkUID int, up bool) error {
	link, ok := GetLinkByUID(linkUID)
	if !ok {
		vxLanOvrlyLogger.Infof("SetLocalLinkState: no vxlan link for uid %d; ignoring", linkUID)
		return nil
	}

	carrierprop.SuppressEcho(int64(linkUID), up)

	vethNs, err := ns.GetNS(link.LocalNetNS)
	if err != nil {
		return fmt.Errorf("SetLocalLinkState: netns %q: %w", link.LocalNetNS, err)
	}
	defer vethNs.Close()

	return vethNs.Do(func(_ ns.NetNS) error {
		l, err := netlink.LinkByName(link.LocalIntfName)
		if err != nil {
			return fmt.Errorf("SetLocalLinkState: iface %q uid %d: %w", link.LocalIntfName, linkUID, err)
		}
		vxLanOvrlyLogger.Infof("SetLocalLinkState: setting %s (uid %d) %s per peer",
			link.LocalIntfName, linkUID, carrierprop.UpStr(up))
		if up {
			return netlink.LinkSetUp(l)
		}
		return netlink.LinkSetDown(l)
	})
}

// ReassertLocalLinkStates pushes current carrier for every tracked VXLAN link.
func ReassertLocalLinkStates() {
	if !carrierprop.Enabled() {
		return
	}
	for _, link := range snapshotLinks() {
		up, err := linkOperUp(link)
		if err != nil {
			continue
		}
		if err := carrierprop.NotifyPeerLinkState(link.PeerNodeIP, &mpb.WireLinkState{
			LinkUid: int64(link.UID),
			Up:      up,
		}); err != nil {
			vxLanOvrlyLogger.Infof("ReassertLocalLinkStates: uid %d notify skipped: %v", link.UID, err)
		}
	}
}

// RegisterFromRemotePod records a VXLAN link after CreateOrUpdate.
func RegisterFromRemotePod(v *mpb.RemotePod) {
	if v == nil {
		return
	}
	RegisterLink(&Link{
		UID:           int(v.Vni) - BaseVNI,
		KubeNs:        v.KubeNs,
		LocalNetNS:    v.NetNs,
		LocalIntfName: v.IntfName,
		PeerNodeIP:    v.PeerVtep,
	})
}

// RegisterFromDef records a VXLAN link from the CNI plugin.
func RegisterFromDef(def *mpb.VxlanLinkDef) {
	if def == nil {
		return
	}
	RegisterLink(&Link{
		UID:           int(def.LinkUid),
		KubeNs:        def.KubeNs,
		LocalNetNS:    def.NetNs,
		LocalIntfName: def.IntfName,
		PeerNodeIP:    def.PeerNodeIp,
	})
}
