package grpcwire

import (
	"testing"
	"time"

	"github.com/networkop/meshnet-cni/internal/carrierprop"
	"github.com/vishvananda/netlink"
)

func TestRawFlagsUp(t *testing.T) {
	up := func(raw uint32) bool {
		return carrierprop.OperUp(&stubLink{attrs: &netlink.LinkAttrs{RawFlags: raw}})
	}
	if !up(carrierprop.IFFRunning) {
		t.Errorf("IFF_RUNNING set should be up")
	}
	if !up(0x1043 | carrierprop.IFFRunning) {
		t.Errorf("flags with IFF_RUNNING should be up")
	}
	if up(0) {
		t.Errorf("no flags should be down")
	}
	if up(0x1003) {
		t.Errorf("admin-up without IFF_RUNNING should be down (no carrier)")
	}
}

func TestOperUpFlagsAndOperStateFallback(t *testing.T) {
	up := func(attrs *netlink.LinkAttrs) bool {
		return carrierprop.OperUp(&stubLink{attrs: attrs})
	}
	if !up(&netlink.LinkAttrs{Flags: carrierprop.IFFRunning}) {
		t.Errorf("Flags IFF_RUNNING should be up")
	}
	if !up(&netlink.LinkAttrs{OperState: netlink.OperUp}) {
		t.Errorf("OperState OperUp should be up")
	}
	if up(&netlink.LinkAttrs{Flags: 0x1003, OperState: netlink.OperDown}) {
		t.Errorf("admin-up without carrier should be down")
	}
}

type stubLink struct {
	attrs *netlink.LinkAttrs
}

func (s *stubLink) Attrs() *netlink.LinkAttrs { return s.attrs }
func (s *stubLink) Type() string              { return "stub" }

func edgeMap(edges []carrierprop.Edge) map[int64]bool {
	m := make(map[int64]bool, len(edges))
	for _, e := range edges {
		m[e.Key] = e.Up
	}
	return m
}

func TestCarrierDebouncer_EmitsOneEdgeAfterDebounce(t *testing.T) {
	d := carrierprop.NewDebouncer(300 * time.Millisecond)
	t0 := time.Unix(100, 0)

	d.Observe(1, false, t0)
	if got := d.Due(t0.Add(200 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("premature edge before debounce: %v", got)
	}
	got := d.Due(t0.Add(300 * time.Millisecond))
	if len(got) != 1 || got[0] != (carrierprop.Edge{Key: 1, Up: false}) {
		t.Fatalf("want one down edge, got %v", got)
	}
	d.Observe(1, false, t0.Add(400*time.Millisecond))
	if got := d.Due(t0.Add(800 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("duplicate edge for unchanged state: %v", got)
	}
}

func TestCarrierDebouncer_CollapsesFlapToFinalState(t *testing.T) {
	d := carrierprop.NewDebouncer(300 * time.Millisecond)
	t0 := time.Unix(100, 0)

	d.Observe(1, true, t0)
	if got := d.Due(t0.Add(300 * time.Millisecond)); edgeMap(got)[1] != true || len(got) != 1 {
		t.Fatalf("baseline up edge expected, got %v", got)
	}

	d.Observe(1, false, t0.Add(400*time.Millisecond))
	d.Observe(1, true, t0.Add(450*time.Millisecond))
	d.Observe(1, false, t0.Add(500*time.Millisecond))

	if got := d.Due(t0.Add(700 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("edge emitted mid-flap: %v", got)
	}
	got := d.Due(t0.Add(800 * time.Millisecond))
	if len(got) != 1 || got[0] != (carrierprop.Edge{Key: 1, Up: false}) {
		t.Fatalf("want single collapsed down edge, got %v", got)
	}
}

func TestCarrierDebouncer_FlapReturningToSameStateEmitsNothing(t *testing.T) {
	d := carrierprop.NewDebouncer(300 * time.Millisecond)
	t0 := time.Unix(100, 0)

	d.Observe(1, true, t0)
	d.Due(t0.Add(300 * time.Millisecond))

	d.Observe(1, false, t0.Add(400*time.Millisecond))
	d.Observe(1, true, t0.Add(450*time.Millisecond))

	if got := d.Due(t0.Add(800 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("no edge expected when state returns to last-reported, got %v", got)
	}
}

func TestCarrierDebouncer_IndependentInterfaces(t *testing.T) {
	d := carrierprop.NewDebouncer(300 * time.Millisecond)
	t0 := time.Unix(100, 0)

	d.Observe(1, false, t0)
	d.Observe(2, true, t0.Add(100*time.Millisecond))

	got := d.Due(t0.Add(300 * time.Millisecond))
	if m := edgeMap(got); len(m) != 1 || m[1] != false {
		t.Fatalf("want only iface 1 down, got %v", got)
	}
	got = d.Due(t0.Add(400 * time.Millisecond))
	if m := edgeMap(got); len(m) != 1 || m[2] != true {
		t.Fatalf("want only iface 2 up, got %v", got)
	}
}

func TestCarrierWatchIfacePrefersHostVeth(t *testing.T) {
	w := &GRPCWire{
		LocalNodeIfaceName: "dnos0eno1-0003",
		LocalPodIfaceName:  "eno1",
	}
	if got := carrierWatchIface(w); got != "dnos0eno1-0003" {
		t.Fatalf("want host veth name, got %q", got)
	}
	w.LocalNodeIfaceName = ""
	if got := carrierWatchIface(w); got != "eno1" {
		t.Fatalf("want pod iface fallback, got %q", got)
	}
}

func TestGetWireByIfIndex(t *testing.T) {
	w := &GRPCWire{
		UID:                7,
		LocalPodNetNS:      "ns-ifidx-test",
		LocalNodeIfaceID:   4242,
		LocalNodeIfaceName: "veth-ifidx-test",
	}
	key := linkKey{namespace: w.LocalPodNetNS, linkUID: w.UID}

	wires.mu.Lock()
	wires.wires[key] = w
	wires.mu.Unlock()
	defer func() {
		wires.mu.Lock()
		delete(wires.wires, key)
		wires.mu.Unlock()
	}()

	got, ok := GetWireByIfIndex(4242)
	if !ok || got != w {
		t.Fatalf("expected to find wire by ifindex 4242, got ok=%t wire=%v", ok, got)
	}
	if _, ok := GetWireByIfIndex(9999); ok {
		t.Fatalf("expected no wire for unknown ifindex 9999")
	}
}

func TestConsumeCarrierEcho(t *testing.T) {
	carrierprop.SuppressEcho(555, false)
	if carrierprop.ConsumeEcho(555, true) {
		t.Fatalf("mismatched state must not be consumed")
	}
	if !carrierprop.ConsumeEcho(555, false) {
		t.Fatalf("matching state should be consumed")
	}
	if carrierprop.ConsumeEcho(555, false) {
		t.Fatalf("echo should be consumed only once")
	}
}
