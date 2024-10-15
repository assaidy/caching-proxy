package main

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// [x] cache on desk
// [ ] cli

// TODO: handle mutex
type DeskCache struct {
	DirPath string
	TTL     time.Duration
}

func NewDeskCache(dirPath, serverPort string, ttl time.Duration) (*DeskCache, error) {
	if err := validateDirPath(dirPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dirPath, fs.FileMode(os.O_RDWR)); err != nil {
		return nil, err
	}
	return &DeskCache{
		DirPath: dirPath,
		TTL:     ttl,
	}, nil
}

func (dc *DeskCache) Set(key string, val *CacheEntry) error {
	entryDir := filepath.Join(dc.DirPath, key)

	err := os.MkdirAll(entryDir, 0755)
	if err != nil {
		log.Fatalf("Error creating directory: %v", err)
	}

	return storeEntryData(entryDir, val)
}

func (dc *DeskCache) Get(key string) (*CacheEntry, bool) {
	statusPath := filepath.Join(dc.DirPath, key, "status")
	headersPath := filepath.Join(dc.DirPath, key, "headers")
	bodyPath := filepath.Join(dc.DirPath, key, "body")

	ce := CacheEntry{Headers: make(http.Header)}
	cont, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, false
	}
	ce.StatusCode, _ = strconv.Atoi(string(cont))

	cont, err = os.ReadFile(headersPath)
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(cont), "\n") {
		header := strings.Split(line, ",")
		key := header[0]
		for _, val := range header[1:] {
			ce.Headers.Add(key, val)
		}
	}

	cont, err = os.ReadFile(bodyPath)
	if err != nil {
		return nil, false
	}
	ce.Body = cont

	return &ce, true
}

func (dc *DeskCache) Clear() error {
	return os.RemoveAll(dc.DirPath)
}

// TODO: store cache entry with time-created
// func (dc *DeskCache) schedualCleanup(time.Duration) {
// }

func storeEntryData(entryDir string, ce *CacheEntry) error {
	statusPath := filepath.Join(entryDir, "status")
	headersPath := filepath.Join(entryDir, "headers")
	bodyPath := filepath.Join(entryDir, "body")
	headersStr := ""
	for k, vv := range ce.Headers {
		headersStr += k
		for _, v := range vv {
			headersStr += "," + v
		}
		headersStr += "\n"
	}

	if err := createAndWriteFile(statusPath, strconv.Itoa(ce.StatusCode)); err != nil {
		return err
	}
	if err := createAndWriteFile(headersPath, headersStr); err != nil {
		return err
	}
	if err := createAndWriteFile(bodyPath, string(ce.Body)); err != nil {
		return err
	}

	return nil
}

func createAndWriteFile(path, content string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = file.WriteString(content)
	if err != nil {
		return err
	}
	return nil
}

func validateDirPath(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("the directory does not exist")
	}
	if err != nil {
		return fmt.Errorf("could not access the directory: %v", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("the path is not a directory")
	}
	return nil
}

type CacheEntry struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
}

type CachingProxyServer struct {
	Port   string
	Origin string
	Cache  *DeskCache
	mu     sync.RWMutex
}

func NewCachingProxyServer(port, origin string, cacheTTL time.Duration) (*CachingProxyServer, error) {
	_ = cacheTTL
	cache, err := NewDeskCache(".", port, 1*time.Hour)
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
		cps.Cache.Set(key, &CacheEntry{
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
