// Package carrierprop implements shared SW-289713 carrier propagation helpers
// used by grpc-wire and VXLAN overlays.
package carrierprop

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mpb "github.com/networkop/meshnet-cni/daemon/proto/meshnet/v1beta1"
	"github.com/networkop/meshnet-cni/utils/wireutil"
)

const (
	Env = "MESHNET_PROPAGATE_CARRIER"
	// IFFRunning reflects operational/carrier up on an interface.
	IFFRunning = 0x40
	Debounce   = 300 * time.Millisecond
)

var enabled bool

func Init() {
	v := strings.TrimSpace(os.Getenv(Env))
	enabled = v == "1" || strings.EqualFold(v, "true")
}

func Enabled() bool { return enabled }

func OperUp(link netlink.Link) bool {
	if link == nil || link.Attrs() == nil {
		return false
	}
	attrs := link.Attrs()
	if attrs.RawFlags&IFFRunning != 0 {
		return true
	}
	// Some drivers only populate Flags / OperState in rtnl dumps.
	if attrs.Flags&IFFRunning != 0 {
		return true
	}
	return attrs.OperState == netlink.OperUp
}

func UpStr(up bool) string {
	if up {
		return "up"
	}
	return "down"
}

var (
	echoMu     sync.Mutex
	echoExpect = map[int64]bool{}
)

func SuppressEcho(key int64, up bool) {
	echoMu.Lock()
	echoExpect[key] = up
	echoMu.Unlock()
}

func ConsumeEcho(key int64, up bool) bool {
	echoMu.Lock()
	defer echoMu.Unlock()
	if exp, ok := echoExpect[key]; ok && exp == up {
		delete(echoExpect, key)
		return true
	}
	return false
}

// Edge is a debounced carrier transition for one tracked key.
type Edge struct {
	Key int64
	Up  bool
}

// Debouncer collapses rapid carrier flaps into at most one edge per stable
// transition.
type Debouncer struct {
	debounce time.Duration
	pending  map[int64]bool
	deadline map[int64]time.Time
	emitted  map[int64]bool
}

func NewDebouncer(debounce time.Duration) *Debouncer {
	return &Debouncer{
		debounce: debounce,
		pending:  map[int64]bool{},
		deadline: map[int64]time.Time{},
		emitted:  map[int64]bool{},
	}
}

func (d *Debouncer) Observe(key int64, up bool, now time.Time) {
	// Carrier watch polls every 100ms; only reset the debounce timer when the
	// sampled oper-state changes. Re-arming on every identical sample would push
	// the deadline forward forever and suppress all edges.
	if prev, ok := d.pending[key]; ok && prev == up {
		return
	}
	d.pending[key] = up
	d.deadline[key] = now.Add(d.debounce)
}

func (d *Debouncer) Due(now time.Time) []Edge {
	var edges []Edge
	for k, dl := range d.deadline {
		if now.Before(dl) {
			continue
		}
		delete(d.deadline, k)
		up := d.pending[k]
		if prev, ok := d.emitted[k]; ok && prev == up {
			continue
		}
		d.emitted[k] = up
		edges = append(edges, Edge{Key: k, Up: up})
	}
	return edges
}

// NotifyPeerLinkState asks the peer meshnet daemon to mirror link state.
func NotifyPeerLinkState(peerIP string, state *mpb.WireLinkState) error {
	url := strings.TrimSpace(fmt.Sprintf("%s:%d", peerIP, wireutil.GRPCDefaultPort))
	conn, err := grpc.Dial(url, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := mpb.NewRemoteClient(conn).SetPeerLinkState(ctx, state)
	if err != nil {
		return err
	}
	if resp == nil || !resp.Response {
		return fmt.Errorf("peer rejected link-state update")
	}
	return nil
}
