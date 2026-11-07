package main

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

// InitProxy 使用显式代理覆盖默认的环境变量代理配置。
// 支持 http:// 和 socks5:// 两种代理协议。
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

// InitLimiter 初始化全局限速器。限速值小于等于 0 时表示不限速。
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

// RateLimitedReader 包装 io.Reader，并通过全局限速器控制读取速度。
type RateLimitedReader struct {
	reader io.Reader
}

// NewRateLimitedReader 创建一个带限速功能的 Reader。
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
