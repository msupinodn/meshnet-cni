package vxlan

import "testing"

func TestLinkMapRegisterGetUnregister(t *testing.T) {
	l := &Link{
		UID:           3,
		KubeNs:        "ns1",
		LocalNetNS:    "/var/run/netns/a",
		LocalIntfName: "eno1",
		PeerNodeIP:    "10.0.0.2",
	}
	RegisterLink(l)
	got, ok := GetLink("ns1", 3)
	if !ok || got != l {
		t.Fatalf("GetLink failed: ok=%v got=%v", ok, got)
	}
	got, ok = GetLinkByUID(3)
	if !ok || got != l {
		t.Fatalf("GetLinkByUID failed: ok=%v got=%v", ok, got)
	}
	UnregisterLink("ns1", 3)
	if _, ok := GetLink("ns1", 3); ok {
		t.Fatal("expected link removed")
	}
}
