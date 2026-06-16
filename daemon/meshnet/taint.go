package meshnet

import (
	"context"
	"net"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// defaultReadinessTaintKey is the taint meshnetd removes from its own node once
// the CNI conflist is installed and the daemon is serving. New nodes must be
// registered with this taint (NoSchedule) so workload pods stay off the node
// until meshnet has chained its plugin into the CNI conflist. Without it, pod
// sandboxes created before the conflist is written come up unwired (eth0 only).
const defaultReadinessTaintKey = "meshnet.networkop.co.uk/agent-not-ready"

const (
	taintRemovalRetries  = 12
	taintRemovalInterval = 5 * time.Second
)

const (
	// readinessPollInterval is how often the readiness gate re-checks the CNI
	// conflist and local gRPC endpoint before clearing the taint.
	readinessPollInterval = 2 * time.Second
	// readinessPollTimeout bounds how long the gate waits for meshnet CNI to
	// become ready. If it elapses the taint is left in place so workload pods
	// keep off a node that cannot yet wire them.
	readinessPollTimeout = 5 * time.Minute
	// servingDialTimeout bounds a single dial of the local gRPC endpoint.
	servingDialTimeout = 2 * time.Second
)

// RemoveReadinessTaintWhenReady waits until meshnet's CNI conflist is present on
// disk AND the local gRPC endpoint is accepting connections, then removes the
// node readiness taint. This ordering matters: removing the taint before the
// conflist is installed lets workload pods schedule onto a node whose sandboxes
// come up with the base CNI only (eth0, no meshnet wiring) — the new-node wiring
// race this gates against. Intended to run in a background goroutine.
func (m *Meshnet) RemoveReadinessTaintWhenReady(ctx context.Context, conflistPath string, grpcPort int) {
	if !m.waitForCNIReady(ctx, conflistPath, grpcPort) {
		mnetdLogger.Errorf("meshnet CNI not ready within %s; leaving readiness taint %q in place on node %q",
			readinessPollTimeout, readinessTaintKey(), localNodeName())
		return
	}
	mnetdLogger.Infof("conflist present + serving, removing taint (conflist=%q grpc=:%d)", conflistPath, grpcPort)
	m.RemoveReadinessTaint(ctx)
}

// waitForCNIReady polls until both the conflist file exists (non-empty) and the
// local gRPC port accepts a connection, or the bounded timeout/ctx elapses.
// Returns true only when both conditions are met.
func (m *Meshnet) waitForCNIReady(ctx context.Context, conflistPath string, grpcPort int) bool {
	deadline := time.Now().Add(readinessPollTimeout)
	announced := false
	for {
		conflistReady := conflistPresent(conflistPath)
		servingReady := conflistReady && grpcServing(grpcPort)
		if conflistReady && servingReady {
			return true
		}
		if !announced {
			mnetdLogger.Infof("waiting for %q before clearing readiness taint (conflist_present=%t grpc_serving=%t)",
				conflistPath, conflistReady, servingReady)
			announced = true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(readinessPollInterval):
		}
	}
}

// conflistPresent reports whether the meshnet CNI conflist exists on disk as a
// non-empty regular file.
func conflistPresent(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Size() > 0
}

// grpcServing reports whether the local gRPC endpoint accepts a TCP connection.
func grpcServing(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), servingDialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// readinessTaintKey returns the taint key meshnetd clears on its own node,
// overridable via MESHNET_READINESS_TAINT_KEY.
func readinessTaintKey() string {
	if v := os.Getenv("MESHNET_READINESS_TAINT_KEY"); v != "" {
		return v
	}
	return defaultReadinessTaintKey
}

// localNodeName returns the name of the node this daemon runs on, from NODE_NAME
// (downward API) and falling back to the hostname.
func localNodeName() string {
	if v := os.Getenv("NODE_NAME"); v != "" {
		return v
	}
	host, _ := os.Hostname()
	return host
}

// RemoveReadinessTaint clears the readiness taint from the local node so that
// workload pods can be scheduled now that meshnet's CNI config is installed and
// the daemon is serving. It is idempotent (no-op when the taint is absent) and
// tolerant of transient API errors: it retries and logs rather than crashing.
func (m *Meshnet) RemoveReadinessTaint(ctx context.Context) {
	nodeName := localNodeName()
	taintKey := readinessTaintKey()
	if nodeName == "" {
		mnetdLogger.Warnf("readiness taint %q not removed: node name is empty (set NODE_NAME)", taintKey)
		return
	}
	for attempt := 1; attempt <= taintRemovalRetries; attempt++ {
		if err := removeNodeTaint(ctx, m.KClient, nodeName, taintKey); err == nil {
			return
		} else {
			mnetdLogger.Warnf("attempt %d/%d: failed to remove readiness taint %q from node %q: %v",
				attempt, taintRemovalRetries, taintKey, nodeName, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(taintRemovalInterval):
		}
	}
	mnetdLogger.Errorf("giving up removing readiness taint %q from node %q after %d attempts; workload pods may stay unschedulable",
		taintKey, nodeName, taintRemovalRetries)
}

// removeNodeTaint removes all taints with the given key from the node, retrying
// on optimistic-concurrency conflicts. It is a no-op if the taint is absent.
func removeNodeTaint(ctx context.Context, client kubernetes.Interface, nodeName, taintKey string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		newTaints, removed := filterTaint(node.Spec.Taints, taintKey)
		if !removed {
			mnetdLogger.Infof("readiness taint %q not present on node %q, nothing to do", taintKey, nodeName)
			return nil
		}
		node.Spec.Taints = newTaints
		if _, err := client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
			return err
		}
		mnetdLogger.Infof("removed readiness taint %q from node %q", taintKey, nodeName)
		return nil
	})
}

// filterTaint returns taints with all entries matching key removed, and whether
// any entry was removed.
func filterTaint(taints []corev1.Taint, key string) ([]corev1.Taint, bool) {
	out := make([]corev1.Taint, 0, len(taints))
	removed := false
	for _, t := range taints {
		if t.Key == key {
			removed = true
			continue
		}
		out = append(out, t)
	}
	return out, removed
}
