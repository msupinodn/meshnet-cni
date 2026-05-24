package grpcwire

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func resetPeerIPState(t *testing.T) {
	t.Helper()
	nodeIPMu.Lock()
	defer nodeIPMu.Unlock()
	nodeClient = nil
	nodeIPSet = nil
	nodeIPCacheExpiry = time.Time{}
}

func nodeWithIPs(name string, internal, external string) *corev1.Node {
	addrs := []corev1.NodeAddress{}
	if internal != "" {
		addrs = append(addrs, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: internal})
	}
	if external != "" {
		addrs = append(addrs, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: external})
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Addresses: addrs},
	}
}

func TestValidatePeerNodeIP_NoClientSkipsValidation(t *testing.T) {
	resetPeerIPState(t)
	if err := validatePeerNodeIP(context.Background(), "1.2.3.4"); err != nil {
		t.Fatalf("expected nil when no client configured, got %v", err)
	}
}

func TestValidatePeerNodeIP_KnownIPAllowed(t *testing.T) {
	resetPeerIPState(t)
	c := fake.NewSimpleClientset(
		nodeWithIPs("n1", "10.0.0.1", ""),
		nodeWithIPs("n2", "10.0.0.2", "203.0.113.5"),
	)
	SetNodeClient(c)
	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "203.0.113.5"} {
		if err := validatePeerNodeIP(context.Background(), ip); err != nil {
			t.Errorf("ip %s should be allowed, got %v", ip, err)
		}
	}
}

func TestValidatePeerNodeIP_UnknownIPRejected(t *testing.T) {
	resetPeerIPState(t)
	c := fake.NewSimpleClientset(nodeWithIPs("n1", "10.0.0.1", ""))
	SetNodeClient(c)
	err := validatePeerNodeIP(context.Background(), "192.0.2.99")
	if err == nil {
		t.Fatal("expected error for IP not in any node's address list")
	}
	if !strings.Contains(err.Error(), "not a known cluster node address") {
		t.Errorf("unexpected error wording: %v", err)
	}
}

func TestValidatePeerNodeIP_CacheRefreshesAfterTTL(t *testing.T) {
	resetPeerIPState(t)
	c := fake.NewSimpleClientset(nodeWithIPs("n1", "10.0.0.1", ""))
	SetNodeClient(c)

	if err := validatePeerNodeIP(context.Background(), "10.0.0.1"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Add a node, force cache expiry, expect the new IP to become reachable.
	if _, err := c.CoreV1().Nodes().Create(context.Background(),
		nodeWithIPs("n2", "10.0.0.2", ""), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	nodeIPMu.Lock()
	nodeIPCacheExpiry = time.Now().Add(-time.Second)
	nodeIPMu.Unlock()

	if err := validatePeerNodeIP(context.Background(), "10.0.0.2"); err != nil {
		t.Errorf("expected refreshed cache to allow 10.0.0.2, got %v", err)
	}
}
