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

// Server struct with a url, status, mutex, and reverse proxy to handle requests
type Server struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

// Server set to maintain list of servers
type Servers struct {
	backends  []*Server
	currentId uint64
}

// Struct to count connections
type ConnectionWatcher struct {
	connections int64
}

// Atomically incrementing ints to count connection attemps and retries
const Attempts int = iota
const Retry int = iota

// Predefined list of servers, this can easily be read from a file if wanted
var ServerList = []string{
	"http://localhost:5000",
	"http://localhost:5002",
	"http://localhost:5003",
	"http://localhost:5004",
	"http://localhost:5005",
}

var serverPool Servers

// Watch state change and increment/decrement connection count appropriately
func (cw *ConnectionWatcher) OnStateChange(conn net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		atomic.AddInt64(&cw.connections, 1)
	case http.StateHijacked, http.StateClosed:
		atomic.AddInt64(&cw.connections, -1)
	}
}

// Function to return the count of connections on server
func (cw *ConnectionWatcher) Count() int {
	fmt.Printf("Total Connections: %d", &cw.connections)
	return int(atomic.LoadInt64(&cw.connections))
}

// Append servers to the server list
func (s *Servers) AddServer(backend *Backend) {
	s.backends = append(s.backends, backend)
}

// Mark the status of a specified server as either alive or dead
func (s *Servers) MarkStatus(url *url.URL, alive bool) {
	for _, back := range s.backends {
		if back.URL.String() == url.String() {
			back.Alive = alive
			return
		}
	}
}

func main() {
	// Initial endpoint which gets load balanced across proxies
	uri, _ := url.Parse("http://127.0.0.1:8080")
	port := 8080

	var cw ConnectionWatcher
	// Initialize server
	server := http.Server{
		ConnState: cw.OnStateChange,
		// Add loadBalancer as function to direct requests
		Handler: http.HandlerFunc(loadBalancer),
		Addr:    fmt.Sprintf(":%d", port),
	}

	// loop through list of servers and attempt to connect to each
	for _, str := range ServerList {
		url, _ := url.Parse(str)
		// proxy is where the request will be redirected to
		proxy := httputil.NewSingleHostReverseProxy(url)

		// Add an error handler to the proxy for when the connection refuses
		proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
			log.Printf("[%s] %s\n", uri.Host, err.Error())
			retries := GetRetryFromContext(request)
			// Attempt to connect to backend 3 times, if not successful, mark it as down
			if retries < 3 {
				select {
				case <-time.After(10 * time.Millisecond):
					// store attempts/retries within context in the request object
					cont := context.WithValue(request.Context(), Retry, retries+1)
					proxy.ServeHTTP(writer, request.WithContext(cont))
				}
				return
			}
			// mark backend as dead
			serverPool.MarkStatus(uri, false)
			// read attempts from context of request
			attempts := GetAttemptsFromContext(request)
			log.Printf("%s(%s) Retrying connection attempt %d", request.URL.Path, attempts)
			cont := context.WithValue(request.Context(), Retry, retries+1)
			loadBalancer(writer, request.WithContext(cont))
		}
		serverPool.AddServer(&Server{
			URL:          url,
			Alive:        true,
			ReverseProxy: proxy,
		})
	}
	go healthCheck()

	log.Printf("Load Balancer started at port: %d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// Return the index of next server
func (s *Servers) NextIndex() int {
	return int(atomic.AddUint64(&s.currentId, uint64(1)) % uint64(len(s.backends)))
}

// Starting at next server look for next alive server. This distributes the load in a round robin manner
func (s *Servers) NextAlive() *Server {
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

// Set server as alive
func (b *Server) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

// Check if server is alive
func (b *Server) IsAlive() (alive bool) {
	b.mux.RLock()
	alive = b.Alive
	b.mux.RUnlock()
	return
}

// Read retries from contett
func GetRetryFromContext(r *http.Request) int {
	if retry, ok := r.Context().Value(Retry).(int); ok {
		return retry
	}
	return 0
}

// Read attempst from context
func GetAttemptsFromContext(r *http.Request) int {
	if attempts, ok := r.Context().Value(Attempts).(int); ok {
		return attempts
	}
	return 0
}

// Serve the response through the next alive server
func loadBalancer(w http.ResponseWriter, r *http.Request) {
	alive := serverPool.NextAlive()
	if alive != nil {
		alive.ReverseProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Service Not Available", http.StatusServiceUnavailable)
}

// Check if server is alive
func isServerAlive(url *url.URL) (alive bool) {
	alive = false
	connection, err := net.DialTimeout("tcp", url.String()[7:], 2*time.Second)
	if err != nil {
		log.Printf("Server: %s", url)
		log.Print(err)
		log.Printf("Server unreachable: %s", url.String())
		return
	}
	alive = true
	_ = connection.Close()
	return
}

// Check the status of each server and log
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

// Periodically run a health check
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
