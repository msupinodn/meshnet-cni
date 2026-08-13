package grpcwire

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	grpcwirev1 "github.com/networkop/meshnet-cni/api/types/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func resetWiresForTest(t *testing.T) {
	t.Helper()
	wires.mu.Lock()
	defer wires.mu.Unlock()
	wires.wires = map[linkKey]*GRPCWire{}
	wires.handles = map[int64]*pcap.Handle{}
}

func TestUpsertGRPCWireItems_ReplacesSameIdentity(t *testing.T) {
	nodeName := must(findNodeName())
	old := grpcwirev1.GWireStatus{
		LocalNodeName:            nodeName,
		LinkId:                   10,
		TopoNamespace:            "topo",
		LocalPodNetNs:            "cni-old",
		LocalPodName:             "pod-a",
		LocalPodIfaceName:        "eth0",
		WireIfaceNameOnLocalNode: "host-old",
		WireIfaceIdOnPeerNode:    100,
	}
	oldItem, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&old)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}

	updated := old
	updated.WireIfaceNameOnLocalNode = "host-new"
	updated.WireIfaceIdOnPeerNode = 200

	got, err := upsertGRPCWireItems([]interface{}{oldItem}, &updated)
	if err != nil {
		t.Fatalf("upsertGRPCWireItems: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 item after upsert, got %d", len(got))
	}

	var stored grpcwirev1.GWireStatus
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(got[0].(map[string]interface{}), &stored); err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if stored.WireIfaceNameOnLocalNode != "host-new" || stored.WireIfaceIdOnPeerNode != 200 {
		t.Errorf("upsert did not replace entry: %+v", stored)
	}
}

func TestUpsertGRPCWireItems_PurgesStaleNetns(t *testing.T) {
	nodeName := must(findNodeName())
	stale := grpcwirev1.GWireStatus{
		LocalNodeName:     nodeName,
		LinkId:            10,
		TopoNamespace:     "topo",
		LocalPodNetNs:     "cni-old",
		LocalPodName:      "pod-a",
		LocalPodIfaceName: "eth0",
	}
	staleItem, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&stale)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}

	fresh := stale
	fresh.LocalPodNetNs = "cni-new"
	fresh.WireIfaceNameOnLocalNode = "host-new"

	got, err := upsertGRPCWireItems([]interface{}{staleItem}, &fresh)
	if err != nil {
		t.Fatalf("upsertGRPCWireItems: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected stale entry replaced by fresh netns, got %d items", len(got))
	}

	var stored grpcwirev1.GWireStatus
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(got[0].(map[string]interface{}), &stored); err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if stored.LocalPodNetNs != "cni-new" {
		t.Errorf("expected fresh netns cni-new, got %q", stored.LocalPodNetNs)
	}
}

func TestK8sStoreGWire_UpsertSameLink(t *testing.T) {
	cs := setUp(t)
	nodeName, err := findNodeName()
	if err != nil {
		t.Fatalf("findNodeName: %v", err)
	}

	wire := &GRPCWire{
		UID:                   42,
		TopoNamespace:         "testNs1",
		LocalPodNetNS:         "cni-abc",
		LocalNodeIfaceName:    "host-v1",
		LocalPodName:          "pod1",
		LocalPodIfaceName:     "eth1",
		LocalPodIP:            "10.0.0.1",
		WireIfaceIDOnPeerNode: 100,
		PeerNodeIP:            "1.2.3.4",
	}
	if err := wire.K8sStoreGWire(); err != nil {
		t.Fatalf("first K8sStoreGWire: %v", err)
	}

	wire.LocalNodeIfaceName = "host-v2"
	wire.WireIfaceIDOnPeerNode = 200
	if err := wire.K8sStoreGWire(); err != nil {
		t.Fatalf("second K8sStoreGWire: %v", err)
	}

	wObjsOnNd, err := cs.Namespace(wire.TopoNamespace).Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gwirekobj: %v", err)
	}
	items, _, _ := unstructured.NestedSlice(wObjsOnNd.Object, kStatus, kGrpcWireItems)
	if len(items) != 1 {
		t.Fatalf("expected 1 grpcWireItem after upsert, got %d", len(items))
	}

	var stored grpcwirev1.GWireStatus
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(items[0].(map[string]interface{}), &stored); err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	want := *CreateWireStatus(wire, nodeName)
	if !cmp.Equal(stored, want) {
		t.Errorf("stored status mismatch:\n%s", cmp.Diff(want, stored))
	}
}

func TestK8sStoreGWire_PodRecycleReplacesStaleNetns(t *testing.T) {
	cs := setUp(t)
	nodeName, err := findNodeName()
	if err != nil {
		t.Fatalf("findNodeName: %v", err)
	}

	oldWire := &GRPCWire{
		UID:                55,
		TopoNamespace:      "testNs1",
		LocalPodNetNS:      "cni-old",
		LocalNodeIfaceName: "host-old",
		LocalPodName:       "pod1",
		LocalPodIfaceName:  "eth1",
		LocalPodIP:         "10.0.0.1",
		PeerNodeIP:         "1.2.3.4",
	}
	if err := oldWire.K8sStoreGWire(); err != nil {
		t.Fatalf("old K8sStoreGWire: %v", err)
	}

	newWire := &GRPCWire{
		UID:                   55,
		TopoNamespace:         "testNs1",
		LocalPodNetNS:         "cni-new",
		LocalNodeIfaceName:    "host-new",
		LocalPodName:          "pod1",
		LocalPodIfaceName:     "eth1",
		LocalPodIP:            "10.0.0.2",
		WireIfaceIDOnPeerNode: 300,
		PeerNodeIP:            "1.2.3.4",
	}
	if err := newWire.K8sStoreGWire(); err != nil {
		t.Fatalf("new K8sStoreGWire: %v", err)
	}

	wObjsOnNd, err := cs.Namespace(newWire.TopoNamespace).Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gwirekobj: %v", err)
	}
	items, _, _ := unstructured.NestedSlice(wObjsOnNd.Object, kStatus, kGrpcWireItems)
	if len(items) != 1 {
		t.Fatalf("expected 1 grpcWireItem after pod recycle, got %d", len(items))
	}

	var stored grpcwirev1.GWireStatus
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(items[0].(map[string]interface{}), &stored); err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if stored.LocalPodNetNs != "cni-new" {
		t.Errorf("expected fresh netns cni-new, got %q", stored.LocalPodNetNs)
	}
}

func TestDeletePodWires_RemovesOrphanedK8sEntries(t *testing.T) {
	nodeName, err := findNodeName()
	if err != nil {
		t.Fatalf("findNodeName: %v", err)
	}

	kobj := &grpcwirev1.GWireKObj{
		TypeMeta: metav1.TypeMeta{
			Kind:       reflect.TypeOf(grpcwirev1.GWireKObj{}).Name(),
			APIVersion: grpcwirev1.GroupName + "/" + grpcwirev1.GroupVersion,
		},
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, Namespace: "orphan-ns"},
		Status: grpcwirev1.GWireKNodeStatus{
			GWireKItems: []grpcwirev1.GWireStatus{{
				LocalNodeName:     nodeName,
				LinkId:            77,
				TopoNamespace:     "orphan-ns",
				LocalPodNetNs:     "cni-stale",
				LocalPodName:      "orphan-pod",
				LocalPodIfaceName: "eth0",
			}},
		},
	}
	cs := setUp(t, kobj)
	resetWiresForTest(t)

	if err := DeletePodWires("orphan-ns", "orphan-pod"); err != nil {
		t.Fatalf("DeletePodWires: %v", err)
	}

	wObjsOnNd, err := cs.Namespace("orphan-ns").Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gwirekobj: %v", err)
	}
	items, _, _ := unstructured.NestedSlice(wObjsOnNd.Object, kStatus, kGrpcWireItems)
	if len(items) != 0 {
		t.Fatalf("expected orphaned K8s entry removed, still have %d items", len(items))
	}
}

func TestReconGWires_PrunesFailedReconEntry(t *testing.T) {
	nodeName, err := findNodeName()
	if err != nil {
		t.Fatalf("findNodeName: %v", err)
	}

	kobj := &grpcwirev1.GWireKObj{
		TypeMeta: metav1.TypeMeta{
			Kind:       reflect.TypeOf(grpcwirev1.GWireKObj{}).Name(),
			APIVersion: grpcwirev1.GroupName + "/" + grpcwirev1.GroupVersion,
		},
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, Namespace: "recon-prune"},
		Status: grpcwirev1.GWireKNodeStatus{
			GWireKItems: []grpcwirev1.GWireStatus{{
				LocalNodeName:            nodeName,
				LinkId:                   88,
				TopoNamespace:            "recon-prune",
				LocalPodNetNs:            "cni-gone",
				LocalPodName:             "gone-pod",
				LocalPodIfaceName:        "eth0",
				WireIfaceNameOnLocalNode: "missing-host-iface",
			}},
		},
	}
	cs := setUp(t, kobj)
	resetWiresForTest(t)

	if err := ReconGWires(); err != nil {
		t.Fatalf("ReconGWires: %v", err)
	}

	wObjsOnNd, err := cs.Namespace("recon-prune").Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gwirekobj: %v", err)
	}
	items, _, _ := unstructured.NestedSlice(wObjsOnNd.Object, kStatus, kGrpcWireItems)
	if len(items) != 0 {
		t.Fatalf("expected failed recon entry pruned from K8s, still have %d items", len(items))
	}
}

func TestUpdateWireByUID_RefreshesPeerIntfIdWhenReady(t *testing.T) {
	InitLogger()
	resetWiresForTest(t)

	w := &GRPCWire{
		UID:                   99,
		LocalPodNetNS:         "ns1",
		TopoNamespace:         "topo",
		IsReady:               true,
		WireIfaceIDOnPeerNode: 307,
	}
	if err := wires.AddInMem(w, nil); err != nil {
		t.Fatalf("AddInMem: %v", err)
	}

	updated, ok := UpdateWireByUID("ns1", 99, 312, make(chan struct{}))
	if !ok {
		t.Fatal("UpdateWireByUID: wire not found")
	}
	if updated.WireIfaceIDOnPeerNode != 312 {
		t.Errorf("WireIfaceIDOnPeerNode = %d, want 312", updated.WireIfaceIDOnPeerNode)
	}
}
