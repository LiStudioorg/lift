// Package s3 实现兼容 S3 的对象存储下载源。
package s3

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/LiStudioorg/lift/internal/core"
)

// Options 保存 S3 连接参数。
type Options struct {
	Endpoint     string
	AccessKey    string
	SecretKey    string
	SessionToken string
	Region       string
}

// Source 从兼容 S3 的存储读取对象。
type Source struct {
	client    *minio.Client
	bucket    string
	key       string
	sourceURL string
}

// New 创建 S3 下载源。
func New(options Options, bucket, key string) (*Source, error) {
	if bucket == "" {
		return nil, fmt.Errorf("S3 Bucket 不能为空")
	}
	if key == "" {
		return nil, fmt.Errorf("S3 对象 Key 不能为空")
	}
	if options.AccessKey == "" || options.SecretKey == "" {
		return nil, fmt.Errorf("S3 需要同时提供 Access Key 和 Secret Key")
	}

	endpoint, secure, err := normalizeEndpoint(options.Endpoint)
	if err != nil {
		return nil, err
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(options.AccessKey, options.SecretKey, options.SessionToken),
		Secure:    secure,
		Region:    options.Region,
		Transport: core.HTTPTransport(),
	})
	if err != nil {
		return nil, fmt.Errorf("创建 S3 客户端失败: %w", err)
	}

	return &Source{
		client:    client,
		bucket:    bucket,
		key:       key,
		sourceURL: "s3://" + bucket + "/" + key,
	}, nil
}

// NewFromURL 从 s3://bucket/key 创建下载源。
func NewFromURL(u *url.URL, options Options) (*Source, error) {
	if u.Host == "" {
		return nil, fmt.Errorf("S3 URL 缺少 Bucket: %s", u.String())
	}
	key := strings.TrimPrefix(u.Path, "/")
	return New(options, u.Host, key)
}

func normalizeEndpoint(raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "s3.amazonaws.com", true, nil
	}

	if !strings.Contains(raw, "://") {
		return strings.TrimSuffix(raw, "/"), true, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("解析 S3 Endpoint 失败: %w", err)
	}
	if u.Host == "" {
		return "", false, fmt.Errorf("S3 Endpoint 缺少主机名: %s", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return "", false, fmt.Errorf("S3 Endpoint 不应包含路径: %s", raw)
	}

	switch strings.ToLower(u.Scheme) {
	case "http":
		return u.Host, false, nil
	case "https":
		return u.Host, true, nil
	default:
		return "", false, fmt.Errorf("不支持的 S3 Endpoint 协议 %q（仅支持 http:// 和 https://）", u.Scheme)
	}
}

// URL 返回下载源地址。
func (s *Source) URL() string {
	return s.sourceURL
}

// Stat 通过 StatObject 获取文件大小和 ETag。
func (s *Source) Stat(ctx context.Context) (*core.ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, s.key, minio.StatObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("S3 StatObject %s/%s 失败: %w", s.bucket, s.key, err)
	}
	return &core.ObjectInfo{Size: info.Size, ETag: info.ETag}, nil
}

// OpenStream 从指定偏移量打开对象数据流。
func (s *Source) OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("S3 GetObject %s/%s: 无效偏移量 %d", s.bucket, s.key, offset)
	}

	options := minio.GetObjectOptions{}
	if offset > 0 {
		if err := options.SetRange(offset, 0); err != nil {
			return nil, fmt.Errorf("S3 设置 Range 失败（偏移 %d）: %w", offset, err)
		}
	}

	object, err := s.client.GetObject(ctx, s.bucket, s.key, options)
	if err != nil {
		return nil, fmt.Errorf("S3 GetObject %s/%s 失败: %w", s.bucket, s.key, err)
	}
	return object, nil
}
