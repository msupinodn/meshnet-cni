# SW-289713 — Propagate physical link-down to the peer datapath port (grpc-wire)

## Problem

In mcDNOS on k8s, two datapath ports on different nodes are connected by a meshnet
**grpc-wire** link. Each link is a veth pair: the pod-side end is `eno<N>`
(DNOS port `ge100-0/0/N`), the node-side (host) end lives in the meshnet daemon
netns and is pcap-captured + tunneled to the peer node over gRPC.

When DNOS takes a port admin-down it runs `ip link set eno<N> down` inside the
pod. The local host-side veth loses carrier and the local port goes oper-down —
but the **peer** port on the other node stays UP: ping dies, yet the peer never
observes link-down. This breaks the AI-Ops RCA correlation scenario, which needs
*both* ends to emit interface-down events (like a real cable pull).

## Root cause

The meshnet daemon is a pure packet forwarder. The per-link goroutine
`RecvFrmLocalPodThread` (`daemon/grpcwire/grpcwire.go:350`) opens the host-side
veth with `pcap.OpenLive(..., pcap.BlockForever)` and `select`s on exactly two
things: a captured packet, or `wire.StopC`. There is **no** `netlink.LinkSubscribe`,
operstate poll, or carrier watch anywhere in the daemon (verified by grep across
the tree). When `eno<N>` goes down the host-side veth simply stops delivering
frames; the goroutine and the gRPC channel stay alive and **nothing** is sent to
the peer daemon, so the peer veth (and peer `eno<N>`) stay UP.

The only existing "down" RPC — `Remote.GRPCWireDownRemote` →
`WireDownByUID` (`grpcwire.go:209`) — only `close(StopC)` (stops forwarding) and
is invoked solely from CNI DEL / pod teardown. It never touches veth link state,
so it cannot be reused for carrier propagation.

## Design (option a — carrier watch + peer link-set)

Opt-in, off by default. When enabled:

1. **Watch** host-side veth carrier via a single shared `netlink.LinkSubscribe`
   dispatcher in the daemon, keyed by ifindex (`LocalNodeIfaceID`, already the key
   of the pcap-handle map in `gwire_map.go`). One subscription covers all wires.
2. **Debounce** (~300ms) and edge-detect operstate. On a stable down/up **edge**
   for a tracked wire, dial the peer (`PeerNodeIP`, port 51111 — same address the
   data path already dials) and call a new RPC `Remote.SetPeerLinkState`.
3. **Apply** on the peer: the handler runs `netlink.LinkSetDown/Up` on that
   wire's host-side veth (`LocalNodeIfaceName`). Downing the host end drops
   carrier on the peer pod's `eno<N>` (NO-CARRIER / lower-layer-down, still
   admin-up) → true cable-pull → peer DNOS port goes oper-down. Up restores it.

### Proto

`daemon/proto/meshnet/v1beta1/meshnet.proto`, `Remote` service:

```
message WireLinkState {
  int64 link_uid = 1;          // informational (logging)
  string local_pod_net_ns = 2; // informational (logging)
  bool up = 3;
  int64 peer_iface_id = 4;     // authoritative: ifindex of the receiver's host-side veth
}
rpc SetPeerLinkState (WireLinkState) returns (BoolResponse);
```

The authoritative wire identity is `peer_iface_id` — the sender's
`WireIfaceIDOnPeerNode`, i.e. the OS ifindex of the peer's host-side veth. The
receiver matches by that ifindex (`GetWireByIfIndex`) exactly as the data path
addresses a packet (`Packet.RemotIntfId` / `GetHostIntfHndl`). This avoids the
cross-namespace uid ambiguity (the sender does not know the peer's pod netns).

Regenerated with the repo's plugins (`protoc-gen-go` v1.36.6, `protoc-gen-go-grpc`
v1.3.0, `paths=source_relative`) — equivalent to `buf generate`.

### Gating

Env `MESHNET_PROPAGATE_CARRIER` (default off). When off the watcher is never
started and no new RPC is ever sent — behavior is byte-for-byte identical to
today. The daemon runs as a DaemonSet, so this must stay opt-in: a change here
affects every meshnet link on every node.

### Robustness

- **Flap:** per-wire debounce + edge detection; at most one RPC per stable edge.
- **Restore:** symmetric — local down→up edge sends `up`, peer does `LinkSetUp`.
  Only the local pod's carrier is mirrored; an `up` never resurrects a peer that
  some other event downed.
- **Daemon restart:** the reconcile path (`ReconGWires` / `reconLocalGRPCWire`)
  re-opens pcap but does NOT force `LinkSetUp`, so a peer-driven `LinkSetDown`
  persists across the peer's restart. On startup, after `ReconGWires`, re-derive
  each local wire's current operstate and re-assert it to the peer so a down is
  never silently forgotten and a stale peer-down is corrected.
- **Reconcile fight:** none — meshnet only sets veths UP once at creation
  (`koko.SetVethLink`); nothing periodically reasserts UP.

## Blast radius

- DaemonSet on every node; gated off by default.
- New RPC is additive; old daemons simply don't implement it. A newer daemon
  calling an older peer gets an Unimplemented error, which is logged and ignored
  (no data-path impact). Mixed-version rollout is safe.
- No change to the packet data path when the feature is off.

## Files touched

- `daemon/proto/meshnet/v1beta1/meshnet.proto` (+ regenerated `*.pb.go`, `*_grpc.pb.go`)
- `daemon/grpcwire/grpcwire.go` — `SetLocalVethState`, peer-notify client helper,
  edge/debounce state on the wire, exported carrier-propagation enable flag.
- `daemon/grpcwire/carrier.go` (new) — the shared `LinkSubscribe` dispatcher +
  debounce + startup re-assert.
- `daemon/meshnet/handler.go` — `SetPeerLinkState` server handler.
- `daemon/main.go` — start the watcher after `ReconGWires` when enabled.

## Test / verification plan

- `go build ./...`, `go vet ./...`.
- Unit: debounce/edge-detection logic (table test: down/up/flap sequences →
  expected emitted edges); `SetLocalVethState` name/lookup error paths.
- Manual/E2E on aks-dap-dev (grpc mode): admin-down `ge100-0/0/99` on one mcDNOS,
  confirm the peer `eno<N>` goes NO-CARRIER and the peer port goes oper-down;
  re-enable and confirm restore; flap a few times and confirm at most one
  transition per edge; restart a daemon and confirm state re-asserts.
- Confirm feature OFF (default) is a no-op: no watcher goroutine, no RPCs.

## Delivered scope (beyond initial grpc-wire design)

### VXLAN carrier propagation (SW-289713 parity)

- `daemon/vxlan/carrier.go` + `daemon/vxlan/link_map.go` — polls in-pod datapath
  iface carrier and drives `SetPeerLinkState` on the peer (same RPC as grpc-wire).
- `RegisterVxlanLink` / `UnregisterVxlanLink` on the Local service; CNI registers
  local links, daemon registers on remote `Update()`.
- Shared debounce/echo-suppression in `internal/carrierprop/` (used by both grpc
  and vxlan watchers).

### mcDNOS datapath defaults (opt-in via DaemonSet env)

| Env | Purpose |
|-----|---------|
| `MESHNET_PROPAGATE_CARRIER=1` | enable carrier propagation (default off) |
| `MESHNET_DATAPATH_IFACE_PREFIX=eno` | leave `eno<N>` admin-down after CNI ADD |
| `MESHNET_DEFAULT_LINK_MTU=9300` | default link MTU when topology omits `mtu` |

meshnetd writes the latter two to `/etc/cni/net.d/` for the CNI plugin.
`MESHNET_DATAPATH_IFACE_PREFIX` unset → legacy (all ifaces up); `""` → all
admin-down; `eno` / `eth` → only matching `<prefix><N>`.

Manifests updated for mcDNOS: `manifests/meshnet-grpc.yaml`,
`manifests/meshnet-vxlan.yaml`, and kustomize overlays `grpc-link/` and
`vxlan-link/`.

### CNI / MTU fixes

- `MESHNET_DEFAULT_LINK_MTU` applied in plugin + vxlan daemon when topology
  omits mtu; VXLAN MTU capped at parent − 50B overlay overhead.
- GRPC wire setup (`plugin/grpcwires-plugin.go`) now uses `linkMTU()` so the
  default MTU applies to `eno<N>` on grpc-wire links too.
- VXLAN cross-node: ignore absent peer `NodeIntf`, discover default-route
  parent; return real errors from remote `Update()` (fixes empty CNI JSON on
  RPC reject).

### Dev verification (devvm kubeadm + kne mcDNOS)

- GRPC carrier propagation: cross-node flap verified (alpine HOST topology).
- VXLAN + mcDNOS: `eno1` mtu 9300, bridge enslavement succeeds with jumbo
  underlay; carrier propagation watcher deployed.
- Note: VXLAN underlay needs parent MTU ≥ 9350 for pod MTU 9300; control-plane
  reachability from workers may need a host route with `mtu 1500` when bond MTU
  is jumbo.
