package http

import (
	"context"
	"fmt"
	"io"
	nethttp "net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/LiStudioorg/lift/internal/core"
)

type mirrorEndpoint struct {
	url    string
	source *Source
}

// mirrorSource 在主 URL 失败时按顺序尝试镜像。
type mirrorSource struct {
	primary    core.Source
	primaryURL string
	mirrors    []mirrorEndpoint

	fallbackOnce sync.Once
	usedMu       sync.Mutex
	used         map[string]bool
}

// ExpandMirrorTemplates 将 {url} 和 {url_encoded} 替换到模板中。不含占位符的模板按前缀处理。
func ExpandMirrorTemplates(templates []string, rawURL string) []string {
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

// NewMirrorSource 为 HTTP 主下载源包装镜像回退能力。
func NewMirrorSource(primary core.Source, rawURL string, templates []string, headers nethttp.Header) (core.Source, error) {
	expanded := ExpandMirrorTemplates(templates, rawURL)
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
			source: NewSource(mirrorURL, headers),
		})
	}

	return &mirrorSource{
		primary:    primary,
		primaryURL: rawURL,
		mirrors:    mirrors,
		used:       make(map[string]bool),
	}, nil
}

func (s *mirrorSource) URL() string {
	return s.primaryURL
}

// Stat 先尝试主下载源，再依次尝试各镜像。
func (s *mirrorSource) Stat(ctx context.Context) (*core.ObjectInfo, error) {
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

// OpenStream 先尝试主下载源，再依次尝试各镜像。
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

func (s *mirrorSource) Close() error {
	if closer, ok := s.primary.(io.Closer); ok {
		_ = closer.Close()
	}
	return nil
}

func (s *mirrorSource) logFallback(err error) {
	s.fallbackOnce.Do(func() {
		core.LogErrorf("\n[镜像] 主 URL 请求失败，尝试镜像: %v\n", err)
	})
}

func (s *mirrorSource) logMirrorUsed(mirrorURL string) {
	s.usedMu.Lock()
	if s.used[mirrorURL] {
		s.usedMu.Unlock()
		return
	}
	s.used[mirrorURL] = true
	s.usedMu.Unlock()

	core.LogErrorf("[镜像] 使用镜像成功: %s\n", mirrorURL)
}
