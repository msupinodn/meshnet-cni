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

// peerNodeIPRetryInterval bounds how often waitForValidPeerNodeIP re-checks a
// peer node IP that is not (yet) a known cluster node address. It is a var so
// tests can shorten it. The node-IP cache itself refreshes on its own TTL
// (nodeIPCacheTTL), so a node that joined via autoscale is recognised within
// at most one TTL window once its Node object becomes observable here.
var peerNodeIPRetryInterval = 5 * time.Second

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

// ValidatePeerNodeIP is the exported form of validatePeerNodeIP, intended
// for callers in other packages (e.g. the meshnet RPC handler that returns
// peer-pod metadata to the local CNI plugin). Behaviour is identical.
func ValidatePeerNodeIP(ctx context.Context, ip string) error {
	return validatePeerNodeIP(ctx, ip)
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

// waitForValidPeerNodeIP blocks until peerIP is a known cluster node address
// (returning nil) or stopC is closed (returning an error).
//
// The peer node IP must be validated before the daemon dials it, but a miss
// must not be fatal to the caller: during autoscale a peer node can be
// referenced by a freshly-created wire before that node's Node object is
// observable on this node. Treating that transient window as a hard failure
// permanently kills the wire's packet-forwarding thread — the cross-node
// tunnel interface stays Admin/Oper UP while silently dropping every frame.
// Polling here lets the forwarding thread start as soon as the node becomes
// known, and still never dials an IP that is not a real cluster node.
func waitForValidPeerNodeIP(ctx context.Context, peerIP string, stopC <-chan struct{}) error {
	for {
		err := validatePeerNodeIP(ctx, peerIP)
		if err == nil {
			return nil
		}
		grpcOvrlyLogger.Infof("[Packet Receive thread] peer node IP %q not yet a known cluster node, retrying in %s: %v",
			peerIP, peerNodeIPRetryInterval, err)
		select {
		case <-stopC:
			return fmt.Errorf("wire stopped while waiting for peer node IP %q to become a known cluster node: last error: %w", peerIP, err)
		case <-time.After(peerNodeIPRetryInterval):
		}
	}
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
