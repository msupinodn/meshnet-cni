package carrierprop

import (
	"os"
	"strings"
	"testing"
)

func skipIfNoNetNS(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if os.IsPermission(err) || strings.Contains(err.Error(), "operation not permitted") {
		t.Skip("setns requires CAP_SYS_ADMIN")
	}
}

func TestOperUpInPodNetNS(t *testing.T) {
	up, err := OperUpInPodNetNS("/proc/self/ns/net", "lo")
	skipIfNoNetNS(t, err)
	if err != nil {
		t.Fatalf("lo in current netns: %v", err)
	}
	if !up {
		t.Fatal("expected lo to be oper up")
	}
}

func TestOperUpInPodNetNS_missingIface(t *testing.T) {
	const missing = "meshnet-carrierprop-test-missing-iface"
	_, err := OperUpInPodNetNS("/proc/self/ns/net", missing)
	skipIfNoNetNS(t, err)
	if err == nil {
		t.Fatalf("expected error for missing interface %q", missing)
	}
}

func TestOperUpInPodNetNS_badNetNS(t *testing.T) {
	_, err := OperUpInPodNetNS("/no/such/netns", "lo")
	if err == nil {
		t.Fatal("expected error for invalid netns path")
	}
}
