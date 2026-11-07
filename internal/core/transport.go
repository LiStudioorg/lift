package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

var httpTransport = &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 100,
	IdleConnTimeout:     90 * time.Second,
}

var httpClient = &http.Client{
	Transport: httpTransport,
}

var globalLimiter *rate.Limiter

// HTTPTransport 返回所有协议共享的传输层。
func HTTPTransport() *http.Transport {
	return httpTransport
}

// HTTPClient 返回共享的 HTTP 客户端。
func HTTPClient() *http.Client {
	return httpClient
}

// Limiter 返回全局限速器；不限速时返回 nil。
func Limiter() *rate.Limiter {
	return globalLimiter
}

// InitProxy 使用显式代理覆盖默认的环境变量代理，支持 http:// 和 socks5://。
func InitProxy(proxyRawURL string) error {
	if proxyRawURL == "" {
		return nil
	}

	proxyURL, err := url.Parse(proxyRawURL)
	if err != nil {
		return fmt.Errorf("解析代理地址失败: %w", err)
	}
	switch proxyURL.Scheme {
	case "http", "socks5":
	default:
		return fmt.Errorf("不支持的代理协议 %q（仅支持 http:// 和 socks5://）", proxyURL.Scheme)
	}
	if proxyURL.Host == "" {
		return fmt.Errorf("代理地址缺少主机名: %s", proxyRawURL)
	}

	httpTransport.Proxy = http.ProxyURL(proxyURL)
	return nil
}

// InitLimiter 初始化全局限速器，值小于等于 0 时关闭限速。
func InitLimiter(limitKBps int) {
	globalLimiter = nil
	if limitKBps <= 0 {
		return
	}

	limit := rate.Limit(limitKBps * 1024)
	burst := limitKBps * 1024
	if burst < 1<<20 {
		burst = 1 << 20
	}
	globalLimiter = rate.NewLimiter(limit, burst)
}

// RateLimitedReader 包装 io.Reader 并应用全局限速。
type RateLimitedReader struct {
	reader io.Reader
}

// NewRateLimitedReader 创建限速读取器。
func NewRateLimitedReader(r io.Reader) *RateLimitedReader {
	return &RateLimitedReader{reader: r}
}

func (r *RateLimitedReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && globalLimiter != nil {
		if waitErr := globalLimiter.WaitN(context.Background(), n); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}
