package meshnet

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClientRateFromEnv_Defaults(t *testing.T) {
	t.Setenv("MESHNET_K8S_QPS", "")
	t.Setenv("MESHNET_K8S_BURST", "")
	qps, burst := clientRateFromEnv()
	if qps != defaultClientQPS {
		t.Errorf("default QPS: got %v want %v", qps, defaultClientQPS)
	}
	if burst != defaultClientBurst {
		t.Errorf("default Burst: got %v want %v", burst, defaultClientBurst)
	}
}

func TestClientRateFromEnv_Override(t *testing.T) {
	t.Setenv("MESHNET_K8S_QPS", "200")
	t.Setenv("MESHNET_K8S_BURST", "400")
	qps, burst := clientRateFromEnv()
	if qps != 200 {
		t.Errorf("override QPS: got %v want 200", qps)
	}
	if burst != 400 {
		t.Errorf("override Burst: got %v want 400", burst)
	}
}

func TestClientRateFromEnv_InvalidIgnored(t *testing.T) {
	t.Setenv("MESHNET_K8S_QPS", "notanumber")
	t.Setenv("MESHNET_K8S_BURST", "-1")
	qps, burst := clientRateFromEnv()
	if qps != defaultClientQPS {
		t.Errorf("invalid QPS should fall back to default: got %v", qps)
	}
	if burst != defaultClientBurst {
		t.Errorf("invalid Burst should fall back to default: got %v", burst)
	}
}

func TestReadinessTaintKey(t *testing.T) {
	t.Setenv("MESHNET_READINESS_TAINT_KEY", "")
	if got := readinessTaintKey(); got != defaultReadinessTaintKey {
		t.Errorf("default taint key: got %q want %q", got, defaultReadinessTaintKey)
	}
	t.Setenv("MESHNET_READINESS_TAINT_KEY", "example.com/custom")
	if got := readinessTaintKey(); got != "example.com/custom" {
		t.Errorf("override taint key: got %q want %q", got, "example.com/custom")
	}
}

func TestFilterTaint(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoSchedule},
		{Key: defaultReadinessTaintKey, Effect: corev1.TaintEffectNoSchedule},
	}
	out, removed := filterTaint(taints, defaultReadinessTaintKey)
	if !removed {
		t.Fatalf("expected taint %q to be removed", defaultReadinessTaintKey)
	}
	if len(out) != 1 || out[0].Key != "node.kubernetes.io/not-ready" {
		t.Fatalf("expected only the not-ready taint to remain, got %+v", out)
	}

	if _, removed := filterTaint(out, defaultReadinessTaintKey); removed {
		t.Fatalf("expected no-op when taint is absent")
	}
}

func TestRemoveNodeTaint(t *testing.T) {
	InitLogger() // removeNodeTaint logs via mnetdLogger, initialised in main()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: defaultReadinessTaintKey, Value: "true", Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}
	client := fake.NewSimpleClientset(node)

	if err := removeNodeTaint(context.Background(), client, "node-1", defaultReadinessTaintKey); err != nil {
		t.Fatalf("removeNodeTaint returned error: %v", err)
	}
	got, err := client.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if len(got.Spec.Taints) != 0 {
		t.Fatalf("expected taints to be cleared, got %+v", got.Spec.Taints)
	}

	// Idempotent: a second call on a node without the taint must succeed.
	if err := removeNodeTaint(context.Background(), client, "node-1", defaultReadinessTaintKey); err != nil {
		t.Fatalf("removeNodeTaint (idempotent) returned error: %v", err)
	}
}
