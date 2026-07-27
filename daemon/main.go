package main

import (
	"context"
	"flag"
	"os"
	"strconv"

	"github.com/networkop/meshnet-cni/daemon/cni"
	"github.com/networkop/meshnet-cni/daemon/grpcwire"
	"github.com/networkop/meshnet-cni/daemon/meshnet"
	"github.com/networkop/meshnet-cni/daemon/vxlan"
	"github.com/networkop/meshnet-cni/utils/wireutil"
	log "github.com/sirupsen/logrus"
)

func main() {

	// On a freshly-joined node meshnetd can start before the node's base CNI
	// conflist is written to /etc/cni/net.d. Wait for it instead of exiting,
	// which previously crash-looped the pod until the base conf appeared.
	if err := cni.WaitForNetConfig(cni.DefaultWaitTimeout, cni.DefaultWaitInterval); err != nil {
		log.Errorf("Failed to initialise CNI plugin: %v", err)
		os.Exit(1)
	}

	if err := cni.Init(); err != nil {
		log.Errorf("Failed to initialise CNI plugin: %v", err)
		os.Exit(1)
	}
	defer cni.Cleanup()

	isDebug := flag.Bool("d", false, "enable degugging")
	grpcPort, err := strconv.Atoi(os.Getenv("GRPC_PORT"))
	if err != nil || grpcPort == 0 {
		grpcPort = wireutil.GRPCDefaultPort
	}
	flag.Parse()
	log.SetLevel(log.InfoLevel)
	if *isDebug {
		log.SetLevel(log.DebugLevel)
		log.Debug("Verbose logging enabled")
	}

	meshnet.InitLogger()
	grpcwire.InitLogger()
	vxlan.InitLogger()

	grpcwire.SeedIndexFromHost()

	m, err := meshnet.New(meshnet.Config{
		Port: grpcPort,
	})
	if err != nil {
		log.Errorf("failed to create meshnet: %v", err)
		os.Exit(1)
	}
	log.Info("Starting meshnet daemon...with grpc support")

	grpcwire.SetGWireClient(m.GWireDynClient)
	grpcwire.SetNodeClient(m.KClient)
	grpcwire.InitCarrierPropagation()

	// read grpcwire info (if any) from data store and update local db
	err = grpcwire.ReconGWires()
	if err != nil {
		log.Errorf("could not reconcile grpc wire: %v", err)
		// generate error and continue
	}

	// SW-289713: re-assert local carrier to peers after recon (so a link that
	// was down before a restart is re-signalled) and start polling in-pod
	// datapath carrier to propagate link-down/up to the peer. Both are no-ops
	// unless MESHNET_PROPAGATE_CARRIER is enabled.
	grpcwire.ReassertLocalLinkStates()
	go grpcwire.StartCarrierWatch(nil)
	vxlan.ReassertLocalLinkStates()
	go vxlan.StartCarrierWatch(nil)

	// Clear the readiness taint that kept workload pods off this node, but only
	// once meshnet CNI can actually wire pods: the conflist must be present on
	// disk AND the gRPC endpoint must be serving. cni.Init wrote the conflist
	// above, but a crash before steady-state serving can leave it removed (see
	// cni.Cleanup), so the gate polls both conditions rather than assuming the
	// synchronous Init above is sufficient. Runs in the background so the gate's
	// bounded wait doesn't delay Serve.
	go m.RemoveReadinessTaintWhenReady(context.Background(), cni.ConflistPath(), grpcPort)

	if err := m.Serve(); err != nil {
		log.Errorf("daemon exited badly: %v", err)
		os.Exit(1)
	}
}
