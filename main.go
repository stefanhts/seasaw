package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
)

type Backend struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy httputil.ReverseProxy
}

type Servers struct {
	backends  []*Backend
	currentId uint64
}

var serverPool Servers

func main() {
	uri, _ := url.Parse("http://localhost:8080")
	rp := httputil.NewSingleHostReverseProxy(uri)

	http.HandlerFunc(rp.ServeHTTP)
}

func (s *Servers) NextIndex() int {
	return int(atomic.AddUint64(&s.currentId, uint64(1)) % uint64(len(s.backends)))
}

func (s *Servers) NextAlive() *Backend {
	nextInd := s.NextIndex()

	loc := nextInd + len(s.backends)
	for i := nextInd; i < loc; i++ {
		ind := i % len(s.backends)

		if s.backends[ind].Alive {
			if i != nextInd {
				atomic.StoreUint64(&s.currentId, uint64(ind))
			}
			return s.backends[ind]
		}
	}
	return nil
}

func (b *Backend) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

func (b *Backend) IsAlive() (alive bool) {
	b.mux.RLock()
	alive = b.Alive
	b.mux.RUnlock()
	return
}

func loadBalancer(w http.ResponseWriter, r *http.Request) {
	alive := serverPool.NextAlive()
	if alive != nil {
		alive.ReverseProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Service Not Available", http.StatusServiceUnavailable)
}
