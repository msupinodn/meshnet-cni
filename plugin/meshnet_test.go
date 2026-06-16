package main

import (
	"context"
	"testing"
	"time"

	mpb "github.com/networkop/meshnet-cni/daemon/proto/meshnet/v1beta1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeLocalClient implements mpb.LocalClient. Only Get is exercised by
// getLocalPodWithRetry; the embedded interface satisfies the rest.
type fakeLocalClient struct {
	mpb.LocalClient
	getFn func() (*mpb.Pod, error)
}

func (f *fakeLocalClient) Get(_ context.Context, _ *mpb.PodQuery, _ ...grpc.CallOption) (*mpb.Pod, error) {
	return f.getFn()
}

func TestGetLocalPodWithRetry_Success(t *testing.T) {
	want := &mpb.Pod{Name: "r1"}
	c := &fakeLocalClient{getFn: func() (*mpb.Pod, error) { return want, nil }}
	got, err := getLocalPodWithRetry(context.Background(), c, "r1", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestGetLocalPodWithRetry_NotFoundReturnsImmediately(t *testing.T) {
	calls := 0
	c := &fakeLocalClient{getFn: func() (*mpb.Pod, error) {
		calls++
		return nil, status.Error(codes.NotFound, "not a topology pod")
	}}
	_, err := getLocalPodWithRetry(context.Background(), c, "r1", "ns")
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got code %v want NotFound", status.Code(err))
	}
	if calls != 1 {
		t.Fatalf("NotFound should not be retried, got %d calls", calls)
	}
}

func TestGetLocalPodWithRetry_RetriesUntilReady(t *testing.T) {
	defer setTiming(2*time.Second, 5*time.Millisecond)()
	want := &mpb.Pod{Name: "r1"}
	calls := 0
	c := &fakeLocalClient{getFn: func() (*mpb.Pod, error) {
		calls++
		if calls < 3 {
			return nil, status.Error(codes.Unavailable, "daemon not ready")
		}
		return want, nil
	}}
	got, err := getLocalPodWithRetry(context.Background(), c, "r1", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want || calls != 3 {
		t.Fatalf("got %v after %d calls, want %v after 3 calls", got, calls, want)
	}
}

func TestGetLocalPodWithRetry_TimesOut(t *testing.T) {
	defer setTiming(20*time.Millisecond, 5*time.Millisecond)()
	c := &fakeLocalClient{getFn: func() (*mpb.Pod, error) {
		return nil, status.Error(codes.Unavailable, "daemon not ready")
	}}
	_, err := getLocalPodWithRetry(context.Background(), c, "r1", "ns")
	if err == nil {
		t.Fatal("expected error after timeout, got nil")
	}
	if status.Code(err) == codes.NotFound {
		t.Fatalf("timeout error must not be NotFound, got %v", err)
	}
}

func setTiming(timeout, interval time.Duration) func() {
	prevTimeout, prevInterval := localDaemonReadyTimeout, localDaemonRetryInterval
	localDaemonReadyTimeout, localDaemonRetryInterval = timeout, interval
	return func() {
		localDaemonReadyTimeout, localDaemonRetryInterval = prevTimeout, prevInterval
	}
}
