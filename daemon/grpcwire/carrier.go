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

// StartCarrierWatch polls carrier on the gwire host veth (host netns, gwire
// netns fallback) and notifies the peer on debounced transitions. On mcDNOS the
// host-side veth (LocalNodeIfaceName / LocalNodeIfaceID) lives in the host
// default netns, not LocalPodNetNS; oper-state is read there first. Falls back
// to LocalPodNetNS and LocalPodIfaceName for standard pods where both veth ends
// share the CNI netns.
// Blocks until stopC is closed (pass nil to run for the daemon's lifetime).
// No-op unless carrier propagation is on.
func StartCarrierWatch(stopC <-chan struct{}) {
	if !carrierprop.Enabled() {
		return
	}
	grpcOvrlyLogger.Infof("StartCarrierWatch: monitoring grpc-wire host veth carrier (host netns, gwire netns fallback)")

	deb := carrierprop.NewDebouncer(carrierprop.Debounce)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stopC:
			return
		case now := <-ticker.C:
			for _, wire := range snapshotWires() {
				up, err := wireDatapathOperUp(wire)
				if err != nil {
					grpcOvrlyLogger.Infof("carrier watch: uid %d ifidx %d oper read err: %v (ns=%q host=%q pod=%q)",
						wire.UID, wire.LocalNodeIfaceID, err, wire.LocalPodNetNS, wire.LocalNodeIfaceName, wire.LocalPodIfaceName)
					continue
				}
				deb.Observe(wire.LocalNodeIfaceID, up, now)
			}
			for _, e := range deb.Due(now) {
				if carrierprop.ConsumeEcho(e.Key, e.Up) {
					continue
				}
				propagateEdge(e.Key, e.Up)
			}
		}
	}
}

// carrierWatchIface returns the interface to poll inside LocalPodNetNS.
func carrierWatchIface(wire *GRPCWire) string {
	if wire.LocalNodeIfaceName != "" {
		return wire.LocalNodeIfaceName
	}
	return wire.LocalPodIfaceName
}

func wireDatapathOperUp(wire *GRPCWire) (bool, error) {
	if wire.LocalNodeIfaceID != 0 {
		up, err := carrierprop.OperUpOnHost(int(wire.LocalNodeIfaceID))
		if err == nil {
			return up, nil
		}
	}
	if wire.LocalNodeIfaceName != "" {
		up, err := carrierprop.OperUpOnHostByName(wire.LocalNodeIfaceName)
		if err == nil {
			return up, nil
		}
	}
	name := carrierWatchIface(wire)
	up, err := carrierprop.OperUpInPodNetNS(wire.LocalPodNetNS, name)
	if err == nil || name == wire.LocalPodIfaceName || wire.LocalPodIfaceName == "" {
		return up, err
	}
	return carrierprop.OperUpInPodNetNS(wire.LocalPodNetNS, wire.LocalPodIfaceName)
}

func propagateEdge(ifindex int64, up bool) {
	wire, ok := GetWireByIfIndex(ifindex)
	if !ok {
		return
	}
	grpcOvrlyLogger.Infof("carrier: local datapath %s/%s (uid %d) went %s; notifying peer %s (peer iface %d)",
		wire.LocalPodIfaceName, wire.LocalNodeIfaceName, wire.UID, carrierprop.UpStr(up), wire.PeerNodeIP, wire.WireIfaceIDOnPeerNode)
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
// in-pod datapath interface and pushes it to the peer.
func ReassertLocalLinkStates() {
	if !carrierprop.Enabled() {
		return
	}
	for _, wire := range snapshotWires() {
		up, err := wireDatapathOperUp(wire)
		if err != nil {
			continue
		}
		if err := notifyPeerLinkState(wire, up); err != nil {
			grpcOvrlyLogger.Infof("ReassertLocalLinkStates: uid %d notify skipped: %v", wire.UID, err)
		}
	}
}
