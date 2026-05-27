package meshnet

import (
	"testing"
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
