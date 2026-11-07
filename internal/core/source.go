// Package core 提供与协议无关的下载引擎、断点续传状态管理、批量下载、限速和完整性校验。
package core

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// ObjectInfo 描述远端对象信息。
type ObjectInfo struct {
	Size int64
	ETag string
}

// Source 是可读取的远端对象。
type Source interface {
	Stat(ctx context.Context) (*ObjectInfo, error)
	OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error)
}

// URLSource 由能够暴露稳定 URL 的下载源实现，该 URL 会写入断点续传状态文件。
type URLSource interface {
	URL() string
}

// CloseSource 在下载源实现 io.Closer 时关闭它。
func CloseSource(src Source) {
	if closer, ok := src.(io.Closer); ok {
		_ = closer.Close()
	}
}

// SafeBaseName 仅保留最后一段路径，防止通过 URL 提供的文件名进行目录穿越。
func SafeBaseName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "download.bin"
	}
	return name
}

// DefaultSourceOutputName 从远端路径生成安全的文件名。
func DefaultSourceOutputName(remotePath string) string {
	return SafeBaseName(path.Base(strings.TrimSuffix(remotePath, "/")))
}

// DefaultOutputName 从 URL 生成安全的文件名。
func DefaultOutputName(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		name := strings.TrimSpace(u.Path)
		if name == "" {
			name = strings.TrimSpace(u.Host)
		}
		if name != "" {
			return DefaultSourceOutputName(name)
		}
	}
	return fmt.Sprintf("download-%d.bin", len(rawURL))
}
