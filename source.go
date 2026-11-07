package main

import (
	"context"
	"io"
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
