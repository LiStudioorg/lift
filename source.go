package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// ObjectInfo 描述远端对象的信息。
type ObjectInfo struct {
	Size int64
	ETag string
}

// Source 表示可读取的远端对象。
type Source interface {
	Stat(ctx context.Context) (*ObjectInfo, error)
	OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error)
}

// sourceOptions 保存各协议数据源所需的连接参数。
type sourceOptions struct {
	s3      s3Options
	sftp    sftpOptions
	headers http.Header
	mirrors []string
}

// appSourceOptions 由 main.go 在解析全局参数后填充。
var appSourceOptions sourceOptions

// newSource 使用全局参数创建数据源。
func newSource(rawURL string) (Source, error) {
	return newSourceWithOptions(rawURL, appSourceOptions)
}

// newSourceWithOptions 根据 URL 协议创建对应的数据源，并接入镜像回退。
func newSourceWithOptions(rawURL string, options sourceOptions) (Source, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析 URL 失败: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "", "http", "https":
		src := NewHTTPSource(rawURL, options.headers)
		if len(options.mirrors) > 0 {
			return newMirrorSource(src, rawURL, options.mirrors, options.headers)
		}
		return src, nil
	case "s3":
		return newS3SourceFromURL(u, options.s3)
	case "sftp":
		return newSFTPSourceFromURL(u, options.sftp)
	default:
		return nil, fmt.Errorf("不支持的协议 %q", u.Scheme)
	}
}

// closeSource 关闭实现了 io.Closer 的数据源。
func closeSource(src Source) {
	if closer, ok := src.(io.Closer); ok {
		_ = closer.Close()
	}
}

// defaultSourceOutputName 根据远端路径推导默认输出文件名。
func defaultSourceOutputName(remotePath string) string {
	return safeBaseName(path.Base(strings.TrimSuffix(remotePath, "/")))
}
