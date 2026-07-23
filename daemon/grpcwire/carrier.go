package grpcwire

import (
	"fmt"
	"time"

	"github.com/networkop/meshnet-cni/internal/carrierprop"
	"github.com/vishvananda/netlink"

	mpb "github.com/networkop/meshnet-cni/daemon/proto/meshnet/v1beta1"
)

// CarrierPropagationEnabled reports whether SW-289713 carrier propagation is on.
func CarrierPropagationEnabled() bool { return carrierprop.Enabled() }

// InitCarrierPropagation reads the opt-in env flag. Must be called once at
// daemon startup (after InitLogger).
func InitCarrierPropagation() {
	carrierprop.Init()
	if carrierprop.Enabled() {
		grpcOvrlyLogger.Infof("carrier propagation enabled (%s)", carrierprop.Env)
	}
}

// StartCarrierWatch subscribes to netlink link events and, on a debounced
// carrier up/down edge of a tracked host-side veth, notifies the peer daemon so
// it mirrors the state onto its own veth. Blocks until stopC is closed (pass
// nil to run for the daemon's lifetime). No-op unless carrier propagation is on.
func StartCarrierWatch(stopC <-chan struct{}) {
	if !carrierprop.Enabled() {
		return
	}

	updates := make(chan netlink.LinkUpdate, 64)
	done := make(chan struct{})
	if err := netlink.LinkSubscribe(updates, done); err != nil {
		grpcOvrlyLogger.Errorf("StartCarrierWatch: LinkSubscribe failed: %v", err)
		return
	}
	defer close(done)
	grpcOvrlyLogger.Infof("StartCarrierWatch: monitoring grpc-wire host-side veth carrier")

	deb := carrierprop.NewDebouncer(carrierprop.Debounce)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stopC:
			return
		case lu := <-updates:
			link := lu.Link
			if link == nil || link.Attrs() == nil {
				continue
			}
			idx := int64(link.Attrs().Index)
			if _, ok := GetWireByIfIndex(idx); !ok {
				continue
			}
			deb.Observe(idx, carrierprop.OperUp(link), time.Now())
		case now := <-ticker.C:
			for _, e := range deb.Due(now) {
				if carrierprop.ConsumeEcho(e.Key, e.Up) {
					continue
				}
				propagateEdge(e.Key, e.Up)
			}
		}
	}
}

func propagateEdge(ifindex int64, up bool) {
	wire, ok := GetWireByIfIndex(ifindex)
	if !ok {
		return
	}
	grpcOvrlyLogger.Infof("carrier: local veth %s (uid %d) went %s; notifying peer %s (peer iface %d)",
		wire.LocalNodeIfaceName, wire.UID, carrierprop.UpStr(up), wire.PeerNodeIP, wire.WireIfaceIDOnPeerNode)
	if err := notifyPeerLinkState(wire, up); err != nil {
		grpcOvrlyLogger.Errorf("carrier: failed to notify peer %s for uid %d: %v", wire.PeerNodeIP, wire.UID, err)
	}
}

func notifyPeerLinkState(wire *GRPCWire, up bool) error {
	return carrierprop.NotifyPeerLinkState(wire.PeerNodeIP, &mpb.WireLinkState{
		LinkUid:       int64(wire.UID),
		LocalPodNetNs: wire.LocalPodNetNS,
		Up:            up,
		PeerIfaceId:   wire.WireIfaceIDOnPeerNode,
	})
}

// SetLocalVethState brings this node's host-side veth up or down, mirroring the
// peer end's carrier.
func SetLocalVethState(ifaceID int64, linkUID int, namespace string, up bool) error {
	wire, ok := GetWireByIfIndex(ifaceID)
	if !ok {
		grpcOvrlyLogger.Infof("SetLocalVethState: no wire for iface id %d (uid %d, ns %q); ignoring", ifaceID, linkUID, namespace)
		return nil
	}
	link, err := netlink.LinkByIndex(int(ifaceID))
	if err != nil {
		return fmt.Errorf("SetLocalVethState: iface id %d (%s, uid %d) not found: %w", ifaceID, wire.LocalNodeIfaceName, linkUID, err)
	}

	carrierprop.SuppressEcho(ifaceID, up)

	grpcOvrlyLogger.Infof("SetLocalVethState: setting %s (iface %d, uid %d) %s per peer",
		wire.LocalNodeIfaceName, ifaceID, linkUID, carrierprop.UpStr(up))
	if up {
		return netlink.LinkSetUp(link)
	}
	return netlink.LinkSetDown(link)
}

// ReassertLocalLinkStates re-reads the current carrier of every tracked
// host-side veth and pushes it to the peer.
func ReassertLocalLinkStates() {
	if !carrierprop.Enabled() {
		return
	}
	for _, wire := range snapshotWires() {
		link, err := netlink.LinkByName(wire.LocalNodeIfaceName)
		if err != nil {
			continue
		}
		if err := notifyPeerLinkState(wire, carrierprop.OperUp(link)); err != nil {
			grpcOvrlyLogger.Infof("ReassertLocalLinkStates: uid %d notify skipped: %v", wire.UID, err)
		}
	}
}
