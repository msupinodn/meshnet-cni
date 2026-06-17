package grpcwire

import (
	"context"
	"fmt"
	"net"

	log "github.com/sirupsen/logrus"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/google/gopacket/pcap"
	mpb "github.com/networkop/meshnet-cni/daemon/proto/meshnet/v1beta1"
	"github.com/networkop/meshnet-cni/utils/wireutil"
	koko "github.com/networkop/meshnet-cni/internal/koko"
	"github.com/vishvananda/netlink"
)

func CreateGRPCWireLocal(ctx context.Context, wireDef *mpb.WireDef) (*mpb.BoolResponse, error) {
	locInf, err := net.InterfaceByName(wireDef.WireIfNameOnLocalNode)
	if err != nil {
		log.WithFields(log.Fields{
			"daemon":  "meshnetd",
			"overlay": "gRPC",
		}).Errorf("[ADD-WIRE:LOCAL-END]For pod %s failed to retrieve interface ID for interface %v. error:%v", wireDef.LocalPodName, wireDef.WireIfNameOnLocalNode, err)
		return &mpb.BoolResponse{Response: false}, err
	}

	// update tx checksuming to off
	err = wireutil.SetTxChecksumOff(wireDef.IntfNameInPod, wireDef.LocalPodNetNs)
	if err != nil {
		log.Errorf("Error in setting tx checksum-off on interface %s, ns %s, pod %s: %v", wireDef.IntfNameInPod, wireDef.LocalPodNetNs, wireDef.LocalPodName, err)
		// generate error and continue
	} else {
		log.Infof("Setting tx checksum-off on interface %s, pod %s is successful", wireDef.IntfNameInPod, wireDef.LocalPodName)
	}

	//Using google gopacket for packet receive. An alternative could be using socket. Not sure it it provides any advantage over gopacket.
	wrHandle, err := pcap.OpenLive(wireDef.WireIfNameOnLocalNode, 65365, true, pcap.BlockForever)
	if err != nil {
		log.WithFields(log.Fields{
			"daemon":  "meshnetd",
			"overlay": "gRPC",
		}).Errorf("[ADD-WIRE:LOCAL-END]Could not open interface for send/recv packets for containers local iface id %d. error:%v", locInf.Index, err)
		return &mpb.BoolResponse{Response: false}, err
	}

	aWire := CreateGWire(locInf.Index, wireDef.WireIfNameOnLocalNode, make(chan struct{}), wireDef)
	aWire.IsReady = false
	aWire.Originator = HOST_CREATED_WIRE
	aWire.OriginatorIP = "unknown"

	// Add the newly created wire in the in memory wire-map and k8S data store
	AddWireInMemNDataStore(aWire, wrHandle)

	log.WithFields(log.Fields{
		"daemon":  "meshnetd",
		"overlay": "gRPC",
	}).Infof("[ADD-WIRE:LOCAL-END]For pod %s@%s, node iface id %d starting the local packet receive thread", wireDef.LocalPodName, wireDef.IntfNameInPod, locInf.Index)
	go func() {
		if err := RecvFrmLocalPodThread(aWire, aWire.LocalNodeIfaceName); err != nil {
			log.Errorf("CreateGRPCWireLocal: RecvFrmLocalPodThread exited with error: %v", err)
		}
	}()

	return &mpb.BoolResponse{Response: true}, nil
}

// A remote peer can tell the local node to create/update the local end of the grpc-wire.
// At the local end if the wire is already created then update the wire properties.
// This updation can happen when a pod is deleted and recreated again. This is not very uncommon in K8S to move
// a pod from node A to node B dynamically
func CreateUpdateGRPCWireRemoteTriggered(wireDef *mpb.WireDef, stopC chan struct{}) (*GRPCWire, error) {

	var err error

	// If this wire is already created, then only update the already created wire properties like stopC.
	// This can happen due to a race between the local and remote peer.
	// This can also happen when a pod in one end of the wire is deleted and created again.
	// In all cases link creation happen only once but it can get updated multiple times.
	grpcWire, ok := UpdateWireByUID(wireDef.LocalPodNetNs, int(wireDef.LinkUid), wireDef.WireIfIdOnPeerNode, stopC)
	if ok {
		grpcOvrlyLogger.Infof("[CREATE-UPDATE-WIRE] At remote end this grpc-wire is already created by %s. Local interface id : %d peer interface id : %d", grpcWire.Originator, grpcWire.LocalNodeIfaceID, grpcWire.WireIfaceIDOnPeerNode)
		return grpcWire, nil
	}

	outIfNm, err := GenNodeIfaceName(wireDef.LocalPodName, wireDef.IntfNameInPod)
	if err != nil {
		return nil, fmt.Errorf("[ADD-WIRE:REMOTE-END] could not get current network namespace: %v", err)
	}

	currNs, err := ns.GetCurrentNS()
	if err != nil {
		return nil, fmt.Errorf("[ADD-WIRE:REMOTE-END] could not get current network namespace: %v", err)
	}

	/* Create the veth to connect the pod with the meshnet daemon running on the node */
	hostEndVeth := koko.VEth{
		NsName:   currNs.Path(),
		LinkName: outIfNm,
	}

	inIfNm := wireDef.IntfNameInPod
	inContainerVeth := koko.VEth{
		NsName:   wireDef.LocalPodNetNs,
		LinkName: inIfNm,
		MTU:      int(wireDef.Mtu),
	}

	if wireDef.LocalPodIp != "" {
		ipAddr, ipSubnet, err := net.ParseCIDR(wireDef.LocalPodIp)
		if err != nil {
			return nil, fmt.Errorf("failed to create remote end of GRPC wire(%s@%s), failed to parse CIDR %s: %w",
				inIfNm, wireDef.LocalPodName, wireDef.LocalPodIp, err)
		}
		inContainerVeth.IPAddr = []net.IPNet{{
			IP:   ipAddr,
			Mask: ipSubnet.Mask,
		}}
	}

	// AR-65093 GAP-3b: remote-end veth creation renames a fresh koko* link to the
	// in-pod interface name (e.g. eno3). If a stale remnant from an earlier
	// PARTIAL/failed ADD is still present in the pod netns, the rename fails with
	// EEXIST and every CNI ADD retry re-collides forever, permanently wedging the
	// pod's wiring (the n36 case). Make the create idempotent, but fail-closed:
	// only remove an interface we can positively classify as a stale remnant of a
	// wire that was never successfully established here; never touch a live,
	// correctly-wired peer interface.
	if err := ensureNoStaleRemoteEnd(inContainerVeth, wireDef); err != nil {
		grpcOvrlyLogger.Errorf("[ADD-WIRE:REMOTE-END] %v", err)
		return nil, err
	}

	if err = koko.MakeVeth(inContainerVeth, hostEndVeth); err != nil {
		grpcOvrlyLogger.Errorf("[ADD-WIRE:REMOTE-END] Error creating vEth pair (in:%s <--> out:%s).  Error-> %s", inIfNm, outIfNm, err)
		return nil, err
	}
	if err := wireutil.SetTxChecksumOff(inContainerVeth.LinkName, inContainerVeth.NsName); err != nil {
		grpcOvrlyLogger.Errorf("Error in setting tx checksum-off on interface %s, pod %s: %v", inContainerVeth.LinkName, wireDef.LocalPodName, err)
		// not returning
	}
	locIface, err := net.InterfaceByName(hostEndVeth.LinkName)
	if err != nil {
		// let the caller handle the error
		grpcOvrlyLogger.Errorf("[ADD-WIRE:REMOTE-END] Remote end could not get interface index for %s. error:%v", hostEndVeth.LinkName, err)
		return nil, err
	}
	grpcOvrlyLogger.Infof("[ADD-WIRE:REMOTE-END] Trigger from %s:%d : Successfully created remote pod to node vEth pair %s@%s <--> %s(%d).",
		wireDef.PeerNodeIp, wireDef.WireIfIdOnPeerNode, inIfNm, wireDef.LocalPodName, outIfNm, locIface.Index)
	aWire := CreateGWire(locIface.Index, hostEndVeth.LinkName, stopC, wireDef)
	/* Utilizing google gopacket for polling for packets from the node. This seems to be the
	   simplest way to get all packets.
	   As an alternative to google gopacket(pcap), a socket based implementation is possible.
	   Not sure if socket based implementation can bring any advantage or not.

	   Near term will replace pcap by socket.
	*/
	wrHandle, err := pcap.OpenLive(hostEndVeth.LinkName, 65365, true, pcap.BlockForever)
	if err != nil {
		// let the caller handle the error
		grpcOvrlyLogger.Errorf("[ADD-WIRE:REMOTE-END] At remote end could not open interface (%d) for sed/recv packets for containers. error:%v", locIface.Index, err)
		return nil, err
	}

	// Add the created wire in the in memory wire-map and k8S data store
	AddWireInMemNDataStore(aWire, wrHandle)

	return aWire, nil
}

// ensureNoStaleRemoteEnd makes remote-end veth creation idempotent and safe.
//
// If the in-pod interface name is not yet taken it is a no-op and the caller
// proceeds with a normal create. If the name is already taken it classifies the
// existing interface, using existing wire bookkeeping, as either:
//
//   - LIVE / established (left untouched, returns an error so the caller does not
//     recreate over it), or
//   - a STALE remnant of this wire's prior failed ADD (removed, returns nil so the
//     caller can recreate the wire cleanly).
//
// Classification is deliberately conservative (fail-safe): unless the interface
// is positively identified as a stale remnant - no live in-memory wire, no
// data-store record for this exact pod incarnation, and the link is actually a
// veth we created - it is NOT deleted. Losing one pod's wiring to a conservative
// no-delete is far preferable to tearing down a healthy peer interface.
func ensureNoStaleRemoteEnd(inPod koko.VEth, wireDef *mpb.WireDef) error {
	exists, isVeth, err := inPodLinkInfo(inPod.NsName, inPod.LinkName)
	if err != nil {
		// Could not inspect the pod netns (e.g. not ready yet). Do not block and
		// never delete here; let koko.MakeVeth run and surface any real error.
		grpcOvrlyLogger.Infof("[ADD-WIRE:REMOTE-END] could not inspect in-pod iface %s in netns %s for link %d (%v); proceeding without stale cleanup",
			inPod.LinkName, inPod.NsName, wireDef.LinkUid, err)
		return nil
	}
	if !exists {
		// Nothing in the way - normal create.
		return nil
	}

	// The in-pod interface name is already taken.

	// (1) Concurrent/duplicate trigger: if this exact wire is already tracked in
	// memory it is live - never delete. (The caller also checks this earlier; this
	// re-check closes the gap with a racing trigger.)
	if _, ok := GetWireByUID(wireDef.LocalPodNetNs, int(wireDef.LinkUid)); ok {
		return fmt.Errorf("in-pod iface %s for link %d already exists and is a live tracked wire; not recreating (fail-safe)",
			inPod.LinkName, wireDef.LinkUid)
	}

	// (2) Not in memory: consult the data-store, the source of truth that recon
	// replays after a restart. A record for this exact pod netns + link means a
	// veth pair was successfully established here before; treat as live (recon
	// will re-adopt it) and do not delete.
	recorded, err := IsWireRecordedInDataStore(context.Background(), wireDef.LocalPodNetNs, wireDef.LinkUid)
	if err != nil {
		return fmt.Errorf("in-pod iface %s for link %d already exists and stale-vs-live could not be determined (%v); not deleting (fail-safe)",
			inPod.LinkName, wireDef.LinkUid, err)
	}
	if recorded {
		return fmt.Errorf("in-pod iface %s for link %d already exists and is recorded as an established wire; not recreating (fail-safe)",
			inPod.LinkName, wireDef.LinkUid)
	}

	// (3) No live in-memory wire and no data-store record for this pod incarnation.
	// The name is taken but the link is not a veth we created: do not delete an
	// unrelated interface.
	if !isVeth {
		return fmt.Errorf("in-pod iface %s for link %d already exists but is not a veth; not deleting (fail-safe)",
			inPod.LinkName, wireDef.LinkUid)
	}

	// Positively classified as a stale remnant of a prior failed ADD. Remove it
	// (this also tears down its half-created host-side peer via veth pair
	// deletion) so the caller can recreate the wire cleanly.
	grpcOvrlyLogger.Infof("[ADD-WIRE:REMOTE-END] removing stale remnant veth %s in pod netns %s for link %d (no live wire, no data-store record) to allow idempotent recreate",
		inPod.LinkName, wireDef.LocalPodNetNs, wireDef.LinkUid)
	stale := koko.VEth{NsName: inPod.NsName, LinkName: inPod.LinkName}
	if err := stale.RemoveVethLink(); err != nil {
		return fmt.Errorf("failed to remove stale remnant veth %s in pod netns %s for link %d: %w",
			inPod.LinkName, wireDef.LocalPodNetNs, wireDef.LinkUid, err)
	}
	return nil
}

// inPodLinkInfo inspects an interface inside the given network namespace and
// reports whether it exists and whether it is a veth. A missing interface is
// reported as (false, false, nil); only genuine namespace access failures are
// returned as an error.
func inPodLinkInfo(nsName, linkName string) (exists bool, isVeth bool, err error) {
	var netNs ns.NetNS
	if nsName == "" {
		netNs, err = ns.GetCurrentNS()
	} else {
		netNs, err = ns.GetNS(nsName)
	}
	if err != nil {
		return false, false, err
	}
	defer netNs.Close()

	err = netNs.Do(func(_ ns.NetNS) error {
		link, lerr := netlink.LinkByName(linkName)
		if lerr != nil {
			// Not found (or unreadable) - treat as not present. The caller never
			// deletes when the interface is reported absent, so this is fail-safe.
			return nil
		}
		exists = true
		_, isVeth = link.(*netlink.Veth)
		return nil
	})
	return exists, isVeth, err
}

// When the remote peer tells the local node to remove the local end of the grpc-wire info
func GRPCWireDownRemoteTriggered(wireDef *mpb.WireDef) error {

	err := WireDownByUID(wireDef.LocalPodNetNs, int(wireDef.LinkUid))
	if err != nil {
		grpcOvrlyLogger.Infof("[WIRE-DOWN] Remote end failed in making down wire end in pod %s@%s,. Link uid : %d",
			wireDef.LocalPodName, wireDef.IntfNameInPod, wireDef.LinkUid)
		return nil
	}

	return nil
}
