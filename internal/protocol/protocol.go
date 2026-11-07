// Package protocol 负责将 URL 路由到对应的协议实现。
package protocol

import (
	"fmt"
	nethttp "net/http"
	"net/url"
	"strings"

	"github.com/LiStudioorg/lift/internal/config"
	"github.com/LiStudioorg/lift/internal/core"
	httpsource "github.com/LiStudioorg/lift/internal/protocol/http"
	"github.com/LiStudioorg/lift/internal/protocol/s3"
	"github.com/LiStudioorg/lift/internal/protocol/sftp"
)

// Options 保存各协议下载源共享的连接参数。
type Options struct {
	S3      s3.Options
	SFTP    sftp.Options
	Headers nethttp.Header
	Mirrors []string
}

// OptionsFromConfig 将 TOML 配置转换为协议参数。
func OptionsFromConfig(cfg *config.Config, headers nethttp.Header, mirrors []string) Options {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return Options{
		S3: s3.Options{
			Endpoint:     cfg.S3.Endpoint,
			AccessKey:    cfg.S3.AccessKey,
			SecretKey:    cfg.S3.SecretKey,
			SessionToken: cfg.S3.SessionToken,
			Region:       cfg.S3.Region,
		},
		SFTP: sftp.Options{
			Host:           cfg.SFTP.Host,
			Port:           cfg.SFTP.Port,
			User:           cfg.SFTP.User,
			Password:       cfg.SFTP.Password,
			KeyPath:        cfg.SFTP.KeyPath,
			KeyPassphrase:  cfg.SFTP.KeyPassphrase,
			KnownHostsPath: cfg.SFTP.KnownHosts,
			Insecure:       cfg.SFTP.Insecure,
		},
		Headers: headers,
		Mirrors: mirrors,
	}
}

// NewSource 按 URL 协议创建下载源，并接入 HTTP 镜像回退。
func NewSource(rawURL string, options Options) (core.Source, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析 URL 失败: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "", "http", "https":
		src := httpsource.NewSource(rawURL, options.Headers)
		if len(options.Mirrors) > 0 {
			return httpsource.NewMirrorSource(src, rawURL, options.Mirrors, options.Headers)
		}
		return src, nil
	case "s3":
		return s3.NewFromURL(u, options.S3)
	case "sftp":
		return sftp.NewFromURL(u, options.SFTP)
	default:
		return nil, fmt.Errorf("不支持的协议 %q", u.Scheme)
	}
}
