package s3

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/LiStudioorg/lift/internal/cli"
	"github.com/LiStudioorg/lift/internal/config"
	"github.com/LiStudioorg/lift/internal/core"
)

// CLI 实现 "lift s3 pull" 子命令。
func CLI(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		s3Usage()
		return 0
	}
	if len(args) == 0 || args[0] != "pull" {
		s3Usage()
		return 2
	}

	fs := flag.NewFlagSet("s3 pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = s3Usage
	bucket := fs.String("bucket", "", "S3 存储桶（Bucket）")
	endpoint := fs.String("endpoint", "", "S3 端点（Endpoint，含协议，如 https://s3.amazonaws.com）")
	accessKey := fs.String("ak", "", "S3 访问密钥（Access Key）")
	secretKey := fs.String("sk", "", "S3 密钥（Secret Key）")
	sessionToken := fs.String("token", "", "S3 临时令牌（Session Token，可选）")
	region := fs.String("region", "", "S3 区域（Region，可选）")
	output := fs.String("o", "", "输出文件名")
	limit := fs.Int("limit", 0, "限速（KB/s，0 表示不限速）")
	proxy := fs.String("proxy", "", "代理地址（支持 http:// 和 socks5://）")

	key, err := cli.ParsePullArgs(fs, args[1:], "对象 key")
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		s3Usage()
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}
	visited := config.VisitedFlags(fs)
	config.ApplyStringDefault(bucket, visited, "bucket", cfg.S3.Bucket)
	config.ApplyStringDefault(endpoint, visited, "endpoint", cfg.S3.Endpoint)
	config.ApplyStringDefault(accessKey, visited, "ak", cfg.S3.AccessKey)
	config.ApplyStringDefault(secretKey, visited, "sk", cfg.S3.SecretKey)
	config.ApplyStringDefault(sessionToken, visited, "token", cfg.S3.SessionToken)
	config.ApplyStringDefault(region, visited, "region", cfg.S3.Region)
	config.ApplyIntDefault(limit, visited, "limit", cfg.Limit)
	config.ApplyStringDefault(proxy, visited, "proxy", cfg.Proxy)

	if *bucket == "" {
		fmt.Fprintln(os.Stderr, "参数错误: 缺少 --bucket")
		s3Usage()
		return 2
	}
	defaultName := core.DefaultSourceOutputName(key)
	if *output == "" {
		*output = defaultName
	}
	if !visited["o"] && cfg.DownloadDir != "" {
		resolved, err := config.ResolveOutput(cfg, "", defaultName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "输出路径错误: %v\n", err)
			return 2
		}
		*output = resolved
	}

	core.InitLimiter(*limit)
	if err := core.InitProxy(*proxy); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	src, err := New(Options{
		Endpoint:     *endpoint,
		AccessKey:    *accessKey,
		SecretKey:    *secretKey,
		SessionToken: *sessionToken,
		Region:       *region,
	}, *bucket, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli.RunSource(ctx, src, *output, "")
}

func s3Usage() {
	fmt.Fprintln(os.Stderr, "用法: lift s3 pull <对象 Key> --bucket <存储桶> --endpoint <地址> --ak <访问密钥> --sk <密钥> [-o 输出文件] [-limit KB/s] [-proxy 代理地址]")
}
