package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type Backend struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

type Servers struct {
	backends  []*Backend
	currentId uint64
}

const Attempts int = iota
const Retry int = iota

var ServerList = []string{
	"http://localhost:5000",
	"http://localhost:5002",
	"http://localhost:5003",
	"http://localhost:5004",
	"http://localhost:5005",
}

var serverPool Servers

func (s *Servers) AddServer(backend *Backend) {
	s.backends = append(s.backends, backend)
}

func (s *Servers) MarkBackendStatus(url *url.URL, alive bool) {
	for _, back := range s.backends {
		if back.URL.String() == url.String() {
			back.Alive = alive
			break
		}
	}
}

func main() {
	uri, _ := url.Parse("http://127.0.0.1:8080")
	port := 8080

	server := http.Server{
		Handler: http.HandlerFunc(loadBalancer),
		Addr:    fmt.Sprintf(":%d", port),
	}

	for _, str := range ServerList {
		url, _ := url.Parse(str)
		fmt.Printf("this is the str: %s, url:", str, url)
		proxy := httputil.NewSingleHostReverseProxy(url)

		proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
			log.Printf("[%s] %s\n", uri.Host, err.Error())
			retries := GetRetryFromContext(request)
			// Attempt to connect to backend 3 times, if not successful, mark it as down
			if retries < 3 {
				select {
				case <-time.After(10 * time.Millisecond):
					cont := context.WithValue(request.Context(), Retry, retries+1)
					proxy.ServeHTTP(writer, request.WithContext(cont))
				}
				return
			}
			// mark backend as dead
			serverPool.MarkBackendStatus(uri, false)

			attempts := GetAttempsFromContext(request)
			log.Printf("%s(%s) Retrying connection attempt %4", request.URL.Path, attempts)
			cont := context.WithValue(request.Context(), Retry, retries+1)
			loadBalancer(writer, request.WithContext(cont))
		}
		serverPool.AddServer(&Backend{
			URL:          url,
			Alive:        true,
			ReverseProxy: proxy,
		})
	}
	go healthCheck()

	log.Printf("Load Balancer started at: %d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
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

func GetRetryFromContext(r *http.Request) int {
	if retry, ok := r.Context().Value(Retry).(int); ok {
		return retry
	}
	return 0
}

func GetAttempsFromContext(r *http.Request) int {
	if attempts, ok := r.Context().Value(Attempts).(int); ok {
		return attempts
	}
	return 0
}

func loadBalancer(w http.ResponseWriter, r *http.Request) {
	alive := serverPool.NextAlive()
	if alive != nil {
		alive.ReverseProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Service Not Available", http.StatusServiceUnavailable)
}

func isServerAlive(url *url.URL) (alive bool) {
	alive = false
	connection, err := net.DialTimeout("tcp", url.String(), 2*time.Second)
	if err != nil {
		log.Printf("Server unreachable: %s", url.String())
		return
	}
	alive = true
	_ = connection.Close()
	return
}

func (s *Servers) HealthCheck() {
	for _, back := range s.backends {
		alive := isServerAlive(back.URL)
		back.Alive = alive
		var status string
		if alive {
			status = "ok"
		} else {
			status = "down"
		}
		log.Printf("%s [status: %s]\n", back.URL.String(), status)
	}
}

func healthCheck() {
	timer := time.NewTimer(20 * time.Second)
	for {
		select {
		case <-timer.C:
			log.Print("Starting health check...\n")
			serverPool.HealthCheck()
			log.Printf("End Health Check...\n")
		}
	}
}
