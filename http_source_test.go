package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// servePayload 提供支持 HEAD 和 Range 的测试文件服务。
func servePayload(w http.ResponseWriter, r *http.Request, payload []byte) {
	w.Header().Set("ETag", `"test-etag"`)
	start := 0
	if rangeHeader := r.Header.Get("Range"); strings.HasPrefix(rangeHeader, "bytes=") {
		value := strings.TrimSuffix(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if parsed, err := strconv.Atoi(value); err == nil {
			start = parsed
		}
		if start < 0 || start > len(payload) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-start))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(payload[start:])
	}
}

func TestHTTPSourceInjectsHeaders(t *testing.T) {
	payload := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	var mu sync.Mutex
	var requests []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" || r.Header.Get("Referer") != "https://example.com/" {
			http.Error(w, "missing custom headers", http.StatusForbidden)
			return
		}
		mu.Lock()
		requests = append(requests, r.Method+"|"+r.Header.Get("Range"))
		mu.Unlock()
		servePayload(w, r, payload)
	}))
	defer server.Close()

	headers := http.Header{
		"X-Test":  {"yes"},
		"Referer": {"https://example.com/"},
	}
	src := NewHTTPSource(server.URL+"/file.bin", headers)

	info, err := src.Stat(context.Background())
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Size != int64(len(payload)) {
		t.Fatalf("Stat size = %d, want %d", info.Size, len(payload))
	}
	if info.ETag != `"test-etag"` {
		t.Fatalf("Stat ETag = %q", info.ETag)
	}

	body, err := src.OpenStream(context.Background(), 10)
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != string(payload[10:]) {
		t.Fatalf("OpenStream data = %q, want %q", data, payload[10:])
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2 (%v)", len(requests), requests)
	}
	if requests[0] != "HEAD|" {
		t.Fatalf("HEAD request headers not recorded correctly: %q", requests[0])
	}
	if requests[1] != "GET|bytes=10-" {
		t.Fatalf("GET request headers not recorded correctly: %q", requests[1])
	}
}
