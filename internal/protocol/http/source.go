// Package http 实现 HTTP/HTTPS 下载源。
package http

import (
	"context"
	"fmt"
	"io"
	nethttp "net/http"

	"github.com/LiStudioorg/lift/internal/core"
)

// Source 通过 HTTP 读取远端对象。
type Source struct {
	url     string
	headers nethttp.Header
}

// NewSource 创建 HTTP 下载源，并将自定义请求头附加到 HEAD/GET 请求。
func NewSource(rawURL string, headers nethttp.Header) *Source {
	return &Source{url: rawURL, headers: headers.Clone()}
}

// URL 返回下载源地址。
func (s *Source) URL() string {
	return s.url
}

func (s *Source) applyHeaders(req *nethttp.Request) {
	for name, values := range s.headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
}

// Stat 使用 HEAD 请求获取文件大小和 ETag。
func (s *Source) Stat(ctx context.Context) (*core.ObjectInfo, error) {
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodHead, s.url, nil)
	if err != nil {
		return nil, err
	}
	s.applyHeaders(req)

	resp, err := core.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < nethttp.StatusOK || resp.StatusCode >= nethttp.StatusMultipleChoices {
		return nil, fmt.Errorf("HEAD %s: 意外的响应状态 %s", s.url, resp.Status)
	}
	if resp.ContentLength < 0 {
		return nil, fmt.Errorf("HEAD %s: 缺少 Content-Length", s.url)
	}

	return &core.ObjectInfo{
		Size: resp.ContentLength,
		ETag: resp.Header.Get("ETag"),
	}, nil
}

// OpenStream 打开 GET 数据流。offset > 0 时请求 Range，并在服务端忽略 Range 时透明跳过相应字节。
func (s *Source) OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("GET %s: 无效的负偏移量 %d", s.url, offset)
	}

	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	s.applyHeaders(req)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := core.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < nethttp.StatusOK || resp.StatusCode >= nethttp.StatusMultipleChoices {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: 意外的响应状态 %s", s.url, resp.Status)
	}
	if offset > 0 {
		switch resp.StatusCode {
		case nethttp.StatusPartialContent:
		case nethttp.StatusOK:
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
