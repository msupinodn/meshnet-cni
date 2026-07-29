package carrierprop

import (
	"testing"
	"time"
)

func TestDebouncer(t *testing.T) {
	d := NewDebouncer(300 * time.Millisecond)
	t0 := time.Unix(100, 0)

	d.Observe(1, false, t0)
	if got := d.Due(t0.Add(200 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("premature edge: %v", got)
	}
	got := d.Due(t0.Add(300 * time.Millisecond))
	if len(got) != 1 || got[0] != (Edge{Key: 1, Up: false}) {
		t.Fatalf("want one down edge, got %v", got)
	}
}

func TestDebouncer_StablePollingDoesNotSlipDeadline(t *testing.T) {
	d := NewDebouncer(300 * time.Millisecond)
	t0 := time.Unix(100, 0)

	d.Observe(1, true, t0)
	for i := 1; i <= 5; i++ {
		d.Observe(1, true, t0.Add(time.Duration(i*100)*time.Millisecond))
	}
	got := d.Due(t0.Add(350 * time.Millisecond))
	if len(got) != 1 || got[0] != (Edge{Key: 1, Up: true}) {
		t.Fatalf("want one up edge despite repeated stable polls, got %v", got)
	}
}

func TestConsumeEcho(t *testing.T) {
	SuppressEcho(9, false)
	if ConsumeEcho(9, true) {
		t.Fatal("mismatched echo must not consume")
	}
	if !ConsumeEcho(9, false) {
		t.Fatal("matching echo should consume")
	}
}
