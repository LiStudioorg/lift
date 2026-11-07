package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// HTTPSource 通过 HTTP 读取远端对象。
type HTTPSource struct {
	url string
}

// NewHTTPSource 创建 HTTP 下载源。
func NewHTTPSource(url string) *HTTPSource {
	return &HTTPSource{url: url}
}

// URL 返回下载源地址。
func (s *HTTPSource) URL() string {
	return s.url
}

// Stat 使用 HEAD 请求获取文件大小和 ETag。
func (s *HTTPSource) Stat(ctx context.Context) (*ObjectInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HEAD %s: unexpected status %s", s.url, resp.Status)
	}
	if resp.ContentLength < 0 {
		return nil, fmt.Errorf("HEAD %s: missing Content-Length", s.url)
	}

	return &ObjectInfo{
		Size: resp.ContentLength,
		ETag: resp.Header.Get("ETag"),
	}, nil
}

// OpenStream 使用 GET 请求打开下载流；offset 大于 0 时通过 Range 头续传。
func (s *HTTPSource) OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("GET %s: negative offset %d", s.url, offset)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: unexpected status %s", s.url, resp.Status)
	}
	if offset > 0 {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			// 服务器正确响应了 Range 请求。
		case http.StatusOK:
			// 服务器忽略 Range 请求时，主动丢弃 offset 之前的数据，
			// 保证返回的流仍然从指定偏移量开始。
			if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
				resp.Body.Close()
				return nil, fmt.Errorf("跳过 %d 字节失败: %w", offset, err)
			}
		default:
			resp.Body.Close()
			return nil, fmt.Errorf("GET %s: 无法从偏移 %d 继续下载（状态 %s）", s.url, offset, resp.Status)
		}
	}

	return resp.Body, nil
}
