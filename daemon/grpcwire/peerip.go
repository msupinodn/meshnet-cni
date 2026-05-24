package grpcwire

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Peer IPs are taken from gwirekobj / topology CRs and fed straight into
// grpc.Dial. RBAC on those CRs can be wide (other controllers write them),
// so the daemon must verify the IP actually belongs to a node in the
// cluster before opening a gRPC connection. Otherwise an attacker with
// write access to a topology CR could steer the daemon to dial arbitrary
// hosts (SSRF, metadata-service pivot, peer impersonation when combined
// with the lack of mTLS).

const nodeIPCacheTTL = 30 * time.Second

var (
	nodeClient        kubernetes.Interface
	nodeIPMu          sync.Mutex
	nodeIPSet         map[string]struct{}
	nodeIPCacheExpiry time.Time
)

// SetNodeClient registers the Kubernetes client used by validatePeerNodeIP
// to enumerate cluster nodes. Called once at daemon startup.
func SetNodeClient(c kubernetes.Interface) {
	nodeIPMu.Lock()
	defer nodeIPMu.Unlock()
	nodeClient = c
	nodeIPSet = nil
	nodeIPCacheExpiry = time.Time{}
}

// validatePeerNodeIP returns nil if ip matches an InternalIP or ExternalIP
// of any node in the cluster. The result is cached for nodeIPCacheTTL.
//
// If no node client has been configured (e.g. unit tests), validation is
// skipped and nil is returned — the daemon binary always sets it, so this
// only relaxes tests, not the production path.
func validatePeerNodeIP(ctx context.Context, ip string) error {
	nodeIPMu.Lock()
	client := nodeClient
	cached := nodeIPSet
	expiry := nodeIPCacheExpiry
	nodeIPMu.Unlock()

	if client == nil {
		return nil
	}

	if cached == nil || time.Now().After(expiry) {
		refreshed, err := refreshNodeIPs(ctx, client)
		if err != nil {
			return fmt.Errorf("refresh node IP allowlist: %w", err)
		}
		cached = refreshed
	}

	if _, ok := cached[ip]; ok {
		return nil
	}
	return fmt.Errorf("peer IP %q is not a known cluster node address", ip)
}

func refreshNodeIPs(ctx context.Context, client kubernetes.Interface) (map[string]struct{}, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(nodes.Items)*2)
	for _, n := range nodes.Items {
		for _, addr := range n.Status.Addresses {
			switch addr.Type {
			case corev1.NodeInternalIP, corev1.NodeExternalIP:
				if addr.Address != "" {
					set[addr.Address] = struct{}{}
				}
			}
		}
	}
	nodeIPMu.Lock()
	nodeIPSet = set
	nodeIPCacheExpiry = time.Now().Add(nodeIPCacheTTL)
	nodeIPMu.Unlock()
	return set, nil
}
