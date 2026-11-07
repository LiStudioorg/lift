package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3Options 是连接 S3 兼容对象存储所需的参数。
type s3Options struct {
	Endpoint     string
	AccessKey    string
	SecretKey    string
	SessionToken string
	Region       string
}

// S3Source 通过 S3 协议读取远端对象。
type S3Source struct {
	client    *minio.Client
	bucket    string
	key       string
	sourceURL string
}

// newS3Source 创建 S3 数据源。
func newS3Source(options s3Options, bucket, key string) (*S3Source, error) {
	if bucket == "" {
		return nil, fmt.Errorf("S3 Bucket 不能为空")
	}
	if key == "" {
		return nil, fmt.Errorf("S3 对象 Key 不能为空")
	}
	if options.AccessKey == "" || options.SecretKey == "" {
		return nil, fmt.Errorf("S3 需要同时提供 Access Key 和 Secret Key")
	}

	endpoint, secure, err := normalizeS3Endpoint(options.Endpoint)
	if err != nil {
		return nil, err
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(options.AccessKey, options.SecretKey, options.SessionToken),
		Secure:    secure,
		Region:    options.Region,
		Transport: httpTransport,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 S3 客户端失败: %w", err)
	}

	return &S3Source{
		client:    client,
		bucket:    bucket,
		key:       key,
		sourceURL: "s3://" + bucket + "/" + key,
	}, nil
}

// newS3SourceFromURL 从 s3://bucket/key 形式的 URL 创建数据源。
func newS3SourceFromURL(u *url.URL, options s3Options) (*S3Source, error) {
	if u.Host == "" {
		return nil, fmt.Errorf("S3 URL 缺少 Bucket: %s", u.String())
	}
	key := strings.TrimPrefix(u.Path, "/")
	return newS3Source(options, u.Host, key)
}

// normalizeS3Endpoint 解析 Endpoint，返回 minio 需要的主机部分和是否使用 HTTPS。
func normalizeS3Endpoint(raw string) (string, bool, error) {
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
func (s *S3Source) URL() string {
	return s.sourceURL
}

// Stat 使用 StatObject 获取对象大小和 ETag。
func (s *S3Source) Stat(ctx context.Context) (*ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, s.key, minio.StatObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("S3 StatObject %s/%s 失败: %w", s.bucket, s.key, err)
	}
	return &ObjectInfo{Size: info.Size, ETag: info.ETag}, nil
}

// OpenStream 使用 GetObjectOptions.SetRange 从指定偏移量打开对象流。
func (s *S3Source) OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error) {
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

// s3CLI 处理 s3 pull 子命令。
func s3CLI(args []string) {
	if len(args) == 0 || args[0] != "pull" {
		s3Usage()
		os.Exit(2)
	}

	fs := flag.NewFlagSet("s3 pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bucket := fs.String("bucket", "", "S3 Bucket")
	endpoint := fs.String("endpoint", "", "S3 Endpoint（含协议，如 https://s3.amazonaws.com）")
	accessKey := fs.String("ak", "", "S3 Access Key")
	secretKey := fs.String("sk", "", "S3 Secret Key")
	sessionToken := fs.String("token", "", "S3 Session Token（可选）")
	region := fs.String("region", "", "S3 Region（可选）")
	output := fs.String("o", "", "输出文件名")
	limit := fs.Int("limit", 0, "限速（KB/s，0 表示不限速）")
	proxy := fs.String("proxy", "", "代理地址（支持 http:// 和 socks5://）")

	key, err := parsePullArgs(fs, args[1:], "对象 key")
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		s3Usage()
		os.Exit(2)
	}
	if *bucket == "" {
		fmt.Fprintln(os.Stderr, "参数错误: 缺少 --bucket")
		s3Usage()
		os.Exit(2)
	}
	if *output == "" {
		*output = defaultSourceOutputName(key)
	}

	InitLimiter(*limit)
	if err := InitProxy(*proxy); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}

	src, err := newS3Source(s3Options{
		Endpoint:     *endpoint,
		AccessKey:    *accessKey,
		SecretKey:    *secretKey,
		SessionToken: *sessionToken,
		Region:       *region,
	}, *bucket, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}

	runSourceCLI(src, *output)
}

func s3Usage() {
	fmt.Fprintln(os.Stderr, "用法: lift s3 pull <key> --bucket <bucket> --endpoint <url> --ak <key> --sk <secret> [-o output] [-limit KB/s] [-proxy URL]")
}
