package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LiStudioorg/lift/internal/core"
	"github.com/LiStudioorg/lift/internal/protocol"
)

// servePayload 提供支持 HEAD 的测试文件服务。
func servePayload(w http.ResponseWriter, r *http.Request, payload []byte) {
	w.Header().Set("ETag", `"test-etag"`)
	w.Header().Set("Content-Length", "29")
	if r.Method != http.MethodHead {
		_, _ = w.Write(payload)
	}
}

func TestListenAPIQueuesAndDownloads(t *testing.T) {
	core.InitLimiter(0)

	payload := []byte("listen api payload 0123456789")
	contentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		servePayload(w, r, payload)
	}))
	defer contentServer.Close()

	outputDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newListenServer(ctx, Options{OutputDir: outputDir}, protocol.Options{})
	defer server.Close()

	apiServer := httptest.NewServer(server.handler())
	defer apiServer.Close()

	requestBody := `{"url":"` + contentServer.URL + `/file.bin"}`
	resp, err := http.Post(apiServer.URL+"/api/download", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("POST /api/download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d, want 202", resp.StatusCode)
	}

	var created listenTask
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response failed: %v", err)
	}
	if created.ID == 0 || created.URL == "" || created.Output == "" {
		t.Fatalf("invalid task response: %+v", created)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("task %d did not finish in time", created.ID)
		}
		tasks := fetchTasks(t, apiServer.URL)
		var current *listenTask
		for i := range tasks {
			if tasks[i].ID == created.ID {
				current = &tasks[i]
				break
			}
		}
		if current == nil {
			t.Fatalf("task %d not found", created.ID)
		}
		switch current.Status {
		case "success":
			data, err := os.ReadFile(current.Output)
			if err != nil {
				t.Fatalf("read downloaded file failed: %v", err)
			}
			if !bytes.Equal(data, payload) {
				t.Fatalf("downloaded data mismatch: %q", data)
			}
			health := fetchHealth(t, apiServer.URL)
			if health["status"] != "ok" {
				t.Fatalf("health = %v", health)
			}
			return
		case "failed", "canceled":
			t.Fatalf("task failed: %+v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestListenAPIRejectsInvalidURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newListenServer(ctx, Options{OutputDir: t.TempDir()}, protocol.Options{})
	defer server.Close()

	apiServer := httptest.NewServer(server.handler())
	defer apiServer.Close()

	resp, err := http.Post(apiServer.URL+"/api/download", "application/json", strings.NewReader(`{"url":"ftp://example.com/file"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func fetchTasks(t *testing.T, baseURL string) []listenTask {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/tasks")
	if err != nil {
		t.Fatalf("GET /api/tasks failed: %v", err)
	}
	defer resp.Body.Close()
	var result struct {
		Tasks []listenTask `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode tasks failed: %v", err)
	}
	return result.Tasks
}

func fetchHealth(t *testing.T, baseURL string) map[string]any {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health failed: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode health failed: %v", err)
	}
	return result
}
