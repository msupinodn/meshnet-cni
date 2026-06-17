package grpcwire

import (
	"context"
	"reflect"
	"testing"

	"github.com/containernetworking/plugins/pkg/ns"
	grpcwirev1 "github.com/networkop/meshnet-cni/api/types/v1beta1"
	mpb "github.com/networkop/meshnet-cni/daemon/proto/meshnet/v1beta1"
	koko "github.com/networkop/meshnet-cni/internal/koko"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestIsWireRecordedInDataStore verifies the stale-vs-live data-store lookup:
// a wire is "recorded" (live/established) only when BOTH the link UID and the
// pod netns match an entry written for this node. This is the bookkeeping that
// the idempotent remote-end recreate relies on to never delete a live wire.
func TestIsWireRecordedInDataStore(t *testing.T) {
	// gWireKObj1 holds links 1 & 2 in netns "testNetNs"; gWireKObj2 holds link 3
	// in netns "testNetNs". Both are owned by this node.
	setUp(t, gWireKObj1, gWireKObj2)

	tests := []struct {
		desc     string
		linkUID  int64
		podNetNs string
		want     bool
	}{
		{"link 1 present in its netns", 1, "testNetNs", true},
		{"link 3 present in its netns", 3, "testNetNs", true},
		{"unknown link uid", 99, "testNetNs", false},
		{"known link uid but wrong netns", 1, "otherNetNs", false},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got, err := IsWireRecordedInDataStore(context.Background(), tc.podNetNs, tc.linkUID)
			if err != nil {
				t.Fatalf("IsWireRecordedInDataStore returned error: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsWireRecordedInDataStore(uid=%d, netns=%q) = %v, want %v",
					tc.linkUID, tc.podNetNs, got, tc.want)
			}
		})
	}
}

// TestEnsureNoStaleRemoteEnd exercises the safety-critical classification:
//   - a stale remnant (no live wire, no data-store record) is removed so the
//     wire can be recreated idempotently (the n36 poison -> recovery), and
//   - a legitimately-wired (live) interface is NEVER deleted, whether its
//     liveness is known from the data-store or from the in-memory wire map.
//
// The interfaces are created in the current network namespace (NsName ""), so
// the test requires root.
func TestEnsureNoStaleRemoteEnd(t *testing.T) {
	isRoot(t)
	InitLogger()

	currNs, err := ns.GetCurrentNS()
	if err != nil {
		t.Fatalf("failed to get current namespace: %v", err)
	}
	defer currNs.Close()

	nodeName, err := findNodeName()
	if err != nil {
		t.Fatalf("could not retrieve node name: %v", err)
	}

	t.Run("stale remnant is removed for idempotent recreate", func(t *testing.T) {
		// gWireKObj2 (link 3, netns "testNetNs") is an unrelated record: it
		// registers the list kind for the fake client but does not match the
		// link under test, so the data-store lookup correctly returns "not found".
		setUp(t, gWireKObj2)

		const ifName = "stale-eno3"
		const peerName = "stale-eno3-h"
		if err := createVethPair(t, ifName, peerName); err != nil {
			t.Fatalf("failed to create stale veth remnant: %v", err)
		}

		wd := &mpb.WireDef{LinkUid: 7001, LocalPodNetNs: ""}
		if err := ensureNoStaleRemoteEnd(koko.VEth{NsName: "", LinkName: ifName}, wd); err != nil {
			// Stale remnant must be cleaned (returns nil) so MakeVeth can recreate.
			// Clean up the leftover before failing.
			_ = cleanupVethPair(t, currNs, ifName)
			t.Fatalf("expected stale remnant to be removed (nil error), got: %v", err)
		}

		exists, _, _ := inPodLinkInfo("", ifName)
		if exists {
			_ = cleanupVethPair(t, currNs, ifName)
			t.Errorf("stale remnant %q should have been deleted to allow idempotent recreate", ifName)
		}
	})

	t.Run("live wire recorded in data-store is never deleted", func(t *testing.T) {
		const ifName = "live-eno3"
		const peerName = "live-eno3-h"

		// Record an established wire for (link 7002, netns "") on this node.
		kobj := &grpcwirev1.GWireKObj{
			TypeMeta: metav1.TypeMeta{
				Kind:       reflect.TypeOf(grpcwirev1.GWireKObj{}).Name(),
				APIVersion: grpcwirev1.GroupName + "/" + grpcwirev1.GroupVersion,
			},
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Namespace: "test"},
			Status: grpcwirev1.GWireKNodeStatus{
				GWireKItems: []grpcwirev1.GWireStatus{{
					LocalNodeName:            nodeName,
					LinkId:                   7002,
					TopoNamespace:            "test",
					LocalPodNetNs:            "",
					LocalPodName:             "live-pod",
					LocalPodIfaceName:        ifName,
					WireIfaceNameOnLocalNode: peerName,
				}},
			},
		}
		setUp(t, kobj)

		if err := createVethPair(t, ifName, peerName); err != nil {
			t.Fatalf("failed to create live veth: %v", err)
		}
		defer cleanupVethPair(t, currNs, ifName)

		wd := &mpb.WireDef{LinkUid: 7002, LocalPodNetNs: ""}
		if err := ensureNoStaleRemoteEnd(koko.VEth{NsName: "", LinkName: ifName}, wd); err == nil {
			t.Fatalf("expected fail-safe error for a data-store-recorded live wire, got nil")
		}

		exists, _, _ := inPodLinkInfo("", ifName)
		if !exists {
			t.Errorf("live (recorded) wire interface %q must NOT be deleted", ifName)
		}
	})

	t.Run("live wire tracked in memory is never deleted", func(t *testing.T) {
		setUp(t) // empty data-store; liveness must be detected from memory

		const ifName = "mem-eno3"
		const peerName = "mem-eno3-h"
		if err := createVethPair(t, ifName, peerName); err != nil {
			t.Fatalf("failed to create in-memory-tracked veth: %v", err)
		}
		defer cleanupVethPair(t, currNs, ifName)

		w := &GRPCWire{
			UID:                7003,
			LocalPodNetNS:      "",
			LocalNodeIfaceName: peerName,
			LocalPodIfaceName:  ifName,
			IsReady:            true,
		}
		if err := wires.AddInMem(w, nil); err != nil {
			t.Fatalf("failed to add wire to in-memory map: %v", err)
		}
		defer func() { _ = wires.AtomicDelete(w) }()

		wd := &mpb.WireDef{LinkUid: 7003, LocalPodNetNs: ""}
		if err := ensureNoStaleRemoteEnd(koko.VEth{NsName: "", LinkName: ifName}, wd); err == nil {
			t.Fatalf("expected fail-safe error for an in-memory tracked wire, got nil")
		}

		exists, _, _ := inPodLinkInfo("", ifName)
		if !exists {
			t.Errorf("in-memory tracked wire interface %q must NOT be deleted", ifName)
		}
	})
}
