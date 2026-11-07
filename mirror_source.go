package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// mirrorEndpoint 是展开后的单个镜像地址。
type mirrorEndpoint struct {
	url    string
	source *HTTPSource
}

// mirrorSource 在主 URL 失败时按顺序尝试镜像。
type mirrorSource struct {
	primary    Source
	primaryURL string
	mirrors    []mirrorEndpoint

	fallbackOnce sync.Once
	usedMu       sync.Mutex
	used         map[string]bool
}

// expandMirrorTemplates 将原始 URL 填入镜像模板。
// 支持 {url}、{url_encoded}；模板不含占位符时按前缀拼接。
func expandMirrorTemplates(templates []string, rawURL string) []string {
	expanded := make([]string, 0, len(templates))
	escaped := url.QueryEscape(rawURL)
	for _, template := range templates {
		template = strings.TrimSpace(template)
		if template == "" {
			continue
		}
		result := template
		result = strings.ReplaceAll(result, "{url_encoded}", escaped)
		result = strings.ReplaceAll(result, "{url}", rawURL)
		if !strings.Contains(template, "{url}") && !strings.Contains(template, "{url_encoded}") {
			result = template + rawURL
		}
		expanded = append(expanded, result)
	}
	return expanded
}

// newMirrorSource 为 HTTP 主源创建镜像回退包装。
func newMirrorSource(primary Source, rawURL string, templates []string, headers http.Header) (Source, error) {
	expanded := expandMirrorTemplates(templates, rawURL)
	if len(expanded) == 0 {
		return primary, nil
	}

	mirrors := make([]mirrorEndpoint, 0, len(expanded))
	for _, mirrorURL := range expanded {
		u, err := url.Parse(mirrorURL)
		if err != nil {
			return nil, fmt.Errorf("解析镜像地址 %q 失败: %w", mirrorURL, err)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
		default:
			return nil, fmt.Errorf("镜像地址仅支持 http/https: %s", mirrorURL)
		}
		mirrors = append(mirrors, mirrorEndpoint{
			url:    mirrorURL,
			source: NewHTTPSource(mirrorURL, headers),
		})
	}

	return &mirrorSource{
		primary:    primary,
		primaryURL: rawURL,
		mirrors:    mirrors,
		used:       make(map[string]bool),
	}, nil
}

// URL 返回主 URL，保证状态文件仍以原始地址作为标识。
func (s *mirrorSource) URL() string {
	return s.primaryURL
}

// Stat 在主源失败时依次尝试镜像。
func (s *mirrorSource) Stat(ctx context.Context) (*ObjectInfo, error) {
	info, err := s.primary.Stat(ctx)
	if err == nil {
		return info, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	s.logFallback(err)

	var lastErr error
	for _, mirror := range s.mirrors {
		info, lastErr = mirror.source.Stat(ctx)
		if lastErr == nil {
			s.logMirrorUsed(mirror.url)
			return info, nil
		}
		if ctx.Err() != nil {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("主 URL 失败: %v；所有镜像均失败: %v", err, lastErr)
}

// OpenStream 在主源失败时依次尝试镜像。
func (s *mirrorSource) OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error) {
	body, err := s.primary.OpenStream(ctx, offset)
	if err == nil {
		return body, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	s.logFallback(err)

	var lastErr error
	for _, mirror := range s.mirrors {
		body, lastErr = mirror.source.OpenStream(ctx, offset)
		if lastErr == nil {
			s.logMirrorUsed(mirror.url)
			return body, nil
		}
		if ctx.Err() != nil {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("主 URL 失败: %v；所有镜像均失败: %v", err, lastErr)
}

// Close 关闭实现了 io.Closer 的主源。
func (s *mirrorSource) Close() error {
	if closer, ok := s.primary.(io.Closer); ok {
		_ = closer.Close()
	}
	return nil
}

// logFallback 只在首次触发回退时输出提示，避免分块下载刷屏。
func (s *mirrorSource) logFallback(err error) {
	s.fallbackOnce.Do(func() {
		outputMu.Lock()
		defer outputMu.Unlock()
		fmt.Fprintf(os.Stderr, "\n[镜像] 主 URL 请求失败，尝试镜像: %v\n", err)
	})
}

// logMirrorUsed 首次使用某个镜像时输出提示。
func (s *mirrorSource) logMirrorUsed(mirrorURL string) {
	s.usedMu.Lock()
	if s.used[mirrorURL] {
		s.usedMu.Unlock()
		return
	}
	s.used[mirrorURL] = true
	s.usedMu.Unlock()

	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Fprintf(os.Stderr, "[镜像] 使用镜像成功: %s\n", mirrorURL)
}
