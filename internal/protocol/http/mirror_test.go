package http

import (
	"context"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/LiStudioorg/lift/internal/core"
)

// newTestSource 根据镜像模板创建带镜像回退的 HTTP 源。
func newTestSource(rawURL string, mirrors []string) (core.Source, error) {
	src := NewSource(rawURL, nil)
	if len(mirrors) == 0 {
		return src, nil
	}
	return NewMirrorSource(src, rawURL, mirrors, nil)
}

func TestMirrorFallbackOnForbidden(t *testing.T) {
	payload := []byte("mirror fallback payload 0123456789")
	var primaryHits atomic.Int64
	var mirrorHits atomic.Int64

	primary := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		primaryHits.Add(1)
		nethttp.Error(w, "forbidden", nethttp.StatusForbidden)
	}))
	defer primary.Close()

	mirror := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mirrorHits.Add(1)
		servePayload(w, r, payload)
	}))
	defer mirror.Close()

	src, err := newTestSource(primary.URL+"/file.bin", []string{mirror.URL + "/{url}"})
	if err != nil {
		t.Fatalf("newSourceWithOptions failed: %v", err)
	}

	info, err := src.Stat(context.Background())
	if err != nil {
		t.Fatalf("Stat via mirror failed: %v", err)
	}
	if info.Size != int64(len(payload)) {
		t.Fatalf("Stat size = %d, want %d", info.Size, len(payload))
	}

	body, err := src.OpenStream(context.Background(), 7)
	if err != nil {
		t.Fatalf("OpenStream via mirror failed: %v", err)
	}
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != string(payload[7:]) {
		t.Fatalf("mirror data = %q, want %q", data, payload[7:])
	}
	if primaryHits.Load() == 0 {
		t.Fatal("primary URL was not attempted")
	}
	if mirrorHits.Load() == 0 {
		t.Fatal("mirror was not attempted")
	}
}

func TestMirrorFallbackOnConnectionFailure(t *testing.T) {
	payload := []byte("connection failure fallback")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddress := listener.Addr().String()
	listener.Close()

	var mirrorHits atomic.Int64
	mirror := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mirrorHits.Add(1)
		servePayload(w, r, payload)
	}))
	defer mirror.Close()

	src, err := newTestSource("http://"+deadAddress+"/file.bin", []string{mirror.URL + "/?target={url_encoded}"})
	if err != nil {
		t.Fatalf("newSourceWithOptions failed: %v", err)
	}

	info, err := src.Stat(context.Background())
	if err != nil {
		t.Fatalf("Stat via mirror failed: %v", err)
	}
	if info.Size != int64(len(payload)) {
		t.Fatalf("Stat size = %d, want %d", info.Size, len(payload))
	}
	if mirrorHits.Load() == 0 {
		t.Fatal("mirror was not attempted")
	}
}

func TestExpandMirrorTemplates(t *testing.T) {
	raw := "https://example.com/a b.bin?x=1"
	got := ExpandMirrorTemplates([]string{
		"https://ghproxy.com/{url}",
		"https://mirror.example/fetch?target={url_encoded}",
		"https://prefix.example/",
	}, raw)

	want := []string{
		"https://ghproxy.com/" + raw,
		"https://mirror.example/fetch?target=https%3A%2F%2Fexample.com%2Fa+b.bin%3Fx%3D1",
		"https://prefix.example/" + raw,
	}
	if len(got) != len(want) {
		t.Fatalf("template count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("template[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
