package cdn

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/Lucino772/envelop/pkg/steam/steampb"
)

// ServerPool holds a filtered list of CDN servers and hands them out
// round-robin. Thread-safe.
type ServerPool struct {
	servers []*steampb.CContentServerDirectory_ServerInfo
	next    atomic.Uint64
	mu      sync.Mutex
}

// NewServerPool keeps only servers whose type is SteamCache or CDN.
func NewServerPool(servers []*steampb.CContentServerDirectory_ServerInfo) (*ServerPool, error) {
	filtered := make([]*steampb.CContentServerDirectory_ServerInfo, 0, len(servers))
	for _, s := range servers {
		t := s.GetType()
		if t == "SteamCache" || t == "CDN" {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("cdn: no usable content servers")
	}
	return &ServerPool{servers: filtered}, nil
}

// Pick returns the next server in round-robin order.
func (p *ServerPool) Pick() *steampb.CContentServerDirectory_ServerInfo {
	idx := p.next.Add(1) - 1
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.servers[int(idx%uint64(len(p.servers)))]
}

func (p *ServerPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.servers)
}
