package vxlan

import "sync"

// VNI offset used by the CNI plugin when allocating VXLAN IDs.
const BaseVNI = 5000

// Link tracks one VXLAN tunnel endpoint inside a pod netns.
type Link struct {
	UID           int
	KubeNs        string
	LocalNetNS    string
	LocalIntfName string
	PeerNodeIP    string
}

type linkKey struct {
	kubeNs string
	uid    int
}

type linkMap struct {
	mu    sync.Mutex
	links map[linkKey]*Link
}

var links = &linkMap{links: map[linkKey]*Link{}}

func RegisterLink(l *Link) {
	if l == nil {
		return
	}
	links.mu.Lock()
	defer links.mu.Unlock()
	links.links[linkKey{kubeNs: l.KubeNs, uid: l.UID}] = l
}

func UnregisterLink(kubeNs string, uid int) {
	links.mu.Lock()
	defer links.mu.Unlock()
	delete(links.links, linkKey{kubeNs: kubeNs, uid: uid})
}

func GetLink(kubeNs string, uid int) (*Link, bool) {
	links.mu.Lock()
	defer links.mu.Unlock()
	l, ok := links.links[linkKey{kubeNs: kubeNs, uid: uid}]
	return l, ok
}

func GetLinkByUID(uid int) (*Link, bool) {
	links.mu.Lock()
	defer links.mu.Unlock()
	for _, l := range links.links {
		if l.UID == uid {
			return l, true
		}
	}
	return nil, false
}

func snapshotLinks() []*Link {
	links.mu.Lock()
	defer links.mu.Unlock()
	out := make([]*Link, 0, len(links.links))
	for _, l := range links.links {
		out = append(out, l)
	}
	return out
}
