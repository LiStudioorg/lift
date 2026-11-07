package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/LiStudioorg/lift/internal/cli"
	"github.com/LiStudioorg/lift/internal/config"
	"github.com/LiStudioorg/lift/internal/core"
	"github.com/LiStudioorg/lift/internal/protocol"
	dockerproto "github.com/LiStudioorg/lift/internal/protocol/docker"
	s3proto "github.com/LiStudioorg/lift/internal/protocol/s3"
	sftpproto "github.com/LiStudioorg/lift/internal/protocol/sftp"
	"github.com/LiStudioorg/lift/internal/server"
	"github.com/LiStudioorg/lift/internal/torrent"
	"github.com/LiStudioorg/lift/internal/upgrade"
	"github.com/LiStudioorg/lift/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "docker":
			return dockerproto.CLI(args[1:])
		case "s3":
			return s3proto.CLI(args[1:])
		case "sftp":
			return sftpproto.CLI(args[1:])
		case "magnet":
			return torrent.CLI(args[1:])
		case "listen":
			return server.CLI(args[1:])
		case "upgrade":
			return upgrade.CLI(args[1:])
		}
	}

	fs := flag.NewFlagSet("lift", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { usage(fs) }

	output := fs.String("o", "", "输出文件名；批量下载时作为输出目录")
	limit := fs.Int("limit", 0, "限速（KB/s，0 表示不限速）")
	listPath := fs.String("f", "", "包含多个 URL 的列表文件（每行一个，忽略空行和 # 注释）")
	expectedSHA256 := fs.String("sha256", "", "下载完成后校验文件的 SHA256（仅支持单个下载任务）")
	proxy := fs.String("proxy", "", "代理地址（支持 http:// 和 socks5://）")
	mirror := fs.String("mirror", "", "镜像模板，逗号分隔，例如 https://ghproxy.com/{url}")
	var cliHeaders config.HeaderList
	fs.Var(&cliHeaders, "H", "自定义请求头，可多次指定，例如 -H \"Cookie: xxx\" -H \"Referer: yyy\"")
	showVersion := fs.Bool("version", false, "显示版本信息")
	showVersionShort := fs.Bool("v", false, "显示版本信息")

	s3Endpoint := fs.String("s3-endpoint", "", "S3 端点（Endpoint，含协议，如 https://s3.amazonaws.com；默认 AWS）")
	s3AccessKey := fs.String("s3-ak", "", "S3 访问密钥（Access Key）")
	s3SecretKey := fs.String("s3-sk", "", "S3 密钥（Secret Key）")
	s3SessionToken := fs.String("s3-token", "", "S3 临时令牌（Session Token，可选）")
	s3Region := fs.String("s3-region", "", "S3 区域（Region，可选）")

	sftpUser := fs.String("sftp-user", "", "SFTP 用户名（URL 中未提供时使用）")
	sftpPassword := fs.String("sftp-pass", "", "SFTP 密码（URL 中未提供时使用）")
	sftpKey := fs.String("sftp-key", "", "SFTP 私钥路径")
	sftpKeyPassphrase := fs.String("sftp-key-pass", "", "SFTP 私钥密码")
	sftpKnownHosts := fs.String("sftp-known-hosts", "", "SFTP known_hosts 路径")
	sftpInsecure := fs.Bool("sftp-insecure", false, "跳过 SFTP 主机密钥校验（不推荐）")

	if err := fs.Parse(cli.ReorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "参数错误，请使用 -h 查看帮助。")
		return 2
	}
	if *showVersion || *showVersionShort {
		fmt.Println(version.String())
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}
	visited := config.VisitedFlags(fs)

	// 优先级：命令行参数 > 配置文件 > 内置默认值。
	config.ApplyIntDefault(limit, visited, "limit", cfg.Limit)
	config.ApplyStringDefault(proxy, visited, "proxy", cfg.Proxy)
	config.ApplyStringDefault(s3Endpoint, visited, "s3-endpoint", cfg.S3.Endpoint)
	config.ApplyStringDefault(s3AccessKey, visited, "s3-ak", cfg.S3.AccessKey)
	config.ApplyStringDefault(s3SecretKey, visited, "s3-sk", cfg.S3.SecretKey)
	config.ApplyStringDefault(s3SessionToken, visited, "s3-token", cfg.S3.SessionToken)
	config.ApplyStringDefault(s3Region, visited, "s3-region", cfg.S3.Region)
	config.ApplyStringDefault(sftpUser, visited, "sftp-user", cfg.SFTP.User)
	config.ApplyStringDefault(sftpPassword, visited, "sftp-pass", cfg.SFTP.Password)
	config.ApplyStringDefault(sftpKey, visited, "sftp-key", cfg.SFTP.KeyPath)
	config.ApplyStringDefault(sftpKeyPassphrase, visited, "sftp-key-pass", cfg.SFTP.KeyPassphrase)
	config.ApplyStringDefault(sftpKnownHosts, visited, "sftp-known-hosts", cfg.SFTP.KnownHosts)
	config.ApplyBoolDefault(sftpInsecure, visited, "sftp-insecure", cfg.SFTP.Insecure)

	headerLines := []string(cliHeaders)
	if !visited["H"] && len(cfg.Headers) > 0 {
		headerLines = cfg.Headers
	}
	headers, err := config.HeadersFromLines(headerLines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}
	mirrors := config.ParseMirrorFlag(*mirror)
	if !visited["mirror"] && len(cfg.Mirrors) > 0 {
		mirrors = cfg.Mirrors
	}

	sourceOptions := protocol.Options{
		S3: s3proto.Options{
			Endpoint:     *s3Endpoint,
			AccessKey:    *s3AccessKey,
			SecretKey:    *s3SecretKey,
			SessionToken: *s3SessionToken,
			Region:       *s3Region,
		},
		SFTP: sftpproto.Options{
			User:           *sftpUser,
			Password:       *sftpPassword,
			KeyPath:        *sftpKey,
			KeyPassphrase:  *sftpKeyPassphrase,
			KnownHostsPath: *sftpKnownHosts,
			Insecure:       *sftpInsecure,
		},
		Headers: headers,
		Mirrors: mirrors,
	}

	urls, err := core.CollectURLs(fs.Args(), *listPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}
	if len(urls) == 0 {
		fs.Usage()
		return 2
	}
	if *expectedSHA256 != "" && len(urls) > 1 {
		fmt.Fprintln(os.Stderr, "参数错误: -sha256 仅支持单个下载任务")
		return 2
	}

	expectedHash := ""
	if *expectedSHA256 != "" {
		expectedHash, err = core.NormalizeSHA256(*expectedSHA256)
		if err != nil {
			fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
			return 2
		}
	}

	if !visited["o"] && cfg.DownloadDir != "" {
		if len(urls) == 1 {
			if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "创建配置下载目录失败: %v\n", err)
				return 2
			}
			*output = filepath.Join(cfg.DownloadDir, core.DefaultOutputName(urls[0]))
		} else {
			*output = cfg.DownloadDir
		}
	}

	tasks, err := core.BuildTasks(urls, *output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	core.InitLimiter(*limit)
	if err := core.InitProxy(*proxy); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	newSource := func(rawURL string) (core.Source, error) {
		return protocol.NewSource(rawURL, sourceOptions)
	}

	if len(tasks) == 1 {
		src, err := newSource(tasks[0].URL)
		if err != nil {
			return cli.HandleDownloadError(err)
		}
		return cli.RunSource(ctx, src, tasks[0].OutputPath, expectedHash)
	}

	results := core.RunBatch(ctx, tasks, newSource)
	success := 0
	failed := 0
	canceled := false
	for _, result := range results {
		if result.Err == nil {
			success++
			continue
		}
		failed++
		if errors.Is(result.Err, context.Canceled) {
			canceled = true
		}
	}
	fmt.Printf("[批量] 汇总: 成功 %d，失败 %d，总计 %d\n", success, failed, len(results))
	if canceled {
		return 130
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func usage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, "lift %s\n", version.Version)
	fmt.Fprintf(out, "用法: %s [选项] <URL> [URL...]\n", os.Args[0])
	fmt.Fprintf(out, "      %s docker pull <image> -o <output.tar>\n", os.Args[0])
	fmt.Fprintf(out, "      %s s3 pull <key> --bucket <bucket> --endpoint <url> --ak <key> --sk <secret> -o <output>\n", os.Args[0])
	fmt.Fprintf(out, "      %s sftp pull <path> --host <host[:port]> --user <user> (--pass <password> | --key <private_key>) -o <output>\n", os.Args[0])
	fmt.Fprintf(out, "      %s magnet <magnet_uri> -o <output_dir>\n", os.Args[0])
	fmt.Fprintf(out, "      %s listen [-host 127.0.0.1] [-port 16800] [-o 下载目录]\n", os.Args[0])
	fmt.Fprintf(out, "      %s upgrade [-f|--force]\n", os.Args[0])
	fmt.Fprintln(out, "支持协议: http(s)://、s3://bucket/key、sftp://user:pass@host:port/path")
	fmt.Fprintln(out, "BT 下载: lift magnet <magnet_uri> -o <输出目录>")
	fmt.Fprintln(out, "配置文件: ~/.lift/config.toml（可用 LIFT_CONFIG 覆盖路径）")
	fmt.Fprintln(out, "版本信息: -v 或 --version")
	fmt.Fprintln(out, "帮助信息: -h 或 --help")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "选项:")
	fs.PrintDefaults()
}
