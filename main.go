package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/assaidy/caches/cache"
)

type CacheEntry struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
}

type CachingProxyServer struct {
	Port   string
	Origin string
	Cache  *cache.TTLCache[string, *CacheEntry]
	mu     sync.RWMutex
}

func NewCachingProxyServer(port, origin string, cacheTTL time.Duration) (*CachingProxyServer, error) {
	cache, err := cache.NewTTL[string, *CacheEntry](cacheTTL, false)
	cache.ScheduleCleanup(context.Background(), cacheTTL)
	if err != nil {
		return nil, fmt.Errorf("couldn't set a cache for the server. error: %v", err)
	}
	return &CachingProxyServer{
		Port:   port,
		Origin: origin,
		Cache:  cache,
	}, nil
}

func copyHeaders(dis, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dis.Add(k, v)
		}
	}
}

func (cps *CachingProxyServer) handleRequests(w http.ResponseWriter, r *http.Request) {
	key := fmt.Sprintf("%s-%s", r.Method, r.URL.Path)

	cps.mu.RLock()
	if val, ok := cps.Cache.Get(key); ok && r.Method == "GET" {
		log.Println("HIT:  ", key)

		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(val.StatusCode)
		copyHeaders(w.Header(), val.Headers)
		w.Write(val.Body)
		cps.mu.RUnlock()
		return
	}
	cps.mu.RUnlock()

	log.Println("MISS: ", key)

	w.Header().Set("X-Cache", "MISS")

	upstreamReq, err := http.NewRequest(r.Method, cps.Origin+r.URL.Path, r.Body)
	if err != nil {
		http.Error(w, "error forwarding request", http.StatusInternalServerError)
		return
	}

	resp, err := http.DefaultClient.Do(upstreamReq)
	if err != nil {
		http.Error(w, "error forwarding request", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "error forwarding request", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(resp.StatusCode)
	copyHeaders(w.Header(), resp.Header)
	w.Write(body)

	if r.Method == "GET" {
		cps.mu.Lock()
		cps.Cache.Put(key, &CacheEntry{
			StatusCode: resp.StatusCode,
			Body:       body,
			Headers:    resp.Header.Clone(),
		})
		cps.mu.Unlock()
	}
	return
}

func (cps *CachingProxyServer) Run() error {
	http.HandleFunc("/", cps.handleRequests)
	return http.ListenAndServe(cps.Port, nil)
}

func main() {
	server, err := NewCachingProxyServer(":8080", "http://dummyjson.com", 1*time.Hour)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("starting caching proxy server at port 8080...")
	log.Fatal(server.Run())
}
