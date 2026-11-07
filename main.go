package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "docker":
			dockerCLI(os.Args[2:])
			return
		case "s3":
			s3CLI(os.Args[2:])
			return
		case "sftp":
			sftpCLI(os.Args[2:])
			return
		case "magnet":
			magnetCLI(os.Args[2:])
			return
		case "listen":
			listenCLI(os.Args[2:])
			return
		}
	}

	output := flag.String("o", "", "输出文件名；批量下载时作为输出目录")
	limit := flag.Int("limit", 0, "限速（KB/s，0 表示不限速）")
	listPath := flag.String("f", "", "包含多个 URL 的列表文件（每行一个，忽略空行和 # 注释）")
	expectedSHA256 := flag.String("sha256", "", "下载完成后校验文件的 SHA256（仅支持单个下载任务）")
	proxy := flag.String("proxy", "", "代理地址（支持 http:// 和 socks5://）")
	mirror := flag.String("mirror", "", "镜像模板，逗号分隔，例如 https://ghproxy.com/{url}")
	var cliHeaders headerList
	flag.Var(&cliHeaders, "H", "自定义请求头，可多次指定，例如 -H \"Cookie: xxx\" -H \"Referer: yyy\"")

	s3Endpoint := flag.String("s3-endpoint", "", "S3 Endpoint（含协议，如 https://s3.amazonaws.com；默认 AWS）")
	s3AccessKey := flag.String("s3-ak", "", "S3 Access Key")
	s3SecretKey := flag.String("s3-sk", "", "S3 Secret Key")
	s3SessionToken := flag.String("s3-token", "", "S3 Session Token（可选）")
	s3Region := flag.String("s3-region", "", "S3 Region（可选）")

	sftpUser := flag.String("sftp-user", "", "SFTP 用户名（URL 中未提供时使用）")
	sftpPassword := flag.String("sftp-pass", "", "SFTP 密码（URL 中未提供时使用）")
	sftpKey := flag.String("sftp-key", "", "SFTP 私钥路径")
	sftpKeyPassphrase := flag.String("sftp-key-pass", "", "SFTP 私钥密码")
	sftpKnownHosts := flag.String("sftp-known-hosts", "", "SFTP known_hosts 路径")
	sftpInsecure := flag.Bool("sftp-insecure", false, "跳过 SFTP 主机密钥校验（不推荐）")

	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "用法: %s [选项] <URL> [URL...]\n", os.Args[0])
		fmt.Fprintf(out, "      %s docker pull <image> -o <output.tar>\n", os.Args[0])
		fmt.Fprintf(out, "      %s s3 pull <key> --bucket <bucket> --endpoint <url> --ak <key> --sk <secret> -o <output>\n", os.Args[0])
		fmt.Fprintf(out, "      %s sftp pull <path> --host <host[:port]> --user <user> (--pass <password> | --key <private_key>) -o <output>\n", os.Args[0])
		fmt.Fprintf(out, "      %s magnet <magnet_uri> -o <output_dir>\n", os.Args[0])
		fmt.Fprintf(out, "      %s listen [-host 127.0.0.1] [-port 16800] [-o 下载目录]\n", os.Args[0])
		fmt.Fprintln(out, "支持协议: http(s)://、s3://bucket/key、sftp://user:pass@host:port/path")
		fmt.Fprintln(out, "BT 下载: lift magnet <magnet_uri> -o <输出目录>")
		fmt.Fprintln(out, "配置文件: ~/.lift/config.json（可用 LIFT_CONFIG 覆盖路径）")
		flag.PrintDefaults()
	}

	// 允许选项出现在 URL 前后，兼容子命令风格的参数顺序。
	if err := flag.CommandLine.Parse(reorderArgs(os.Args[1:])); err != nil {
		os.Exit(2)
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(2)
	}
	visited := visitedFlags(flag.CommandLine)

	// 优先级：命令行参数 > 配置文件 > 默认值。
	applyIntDefault(limit, visited, "limit", cfg.Limit)
	applyStringDefault(proxy, visited, "proxy", cfg.Proxy)
	applyStringDefault(s3Endpoint, visited, "s3-endpoint", cfg.S3.Endpoint)
	applyStringDefault(s3AccessKey, visited, "s3-ak", cfg.S3.AccessKey)
	applyStringDefault(s3SecretKey, visited, "s3-sk", cfg.S3.SecretKey)
	applyStringDefault(s3SessionToken, visited, "s3-token", cfg.S3.SessionToken)
	applyStringDefault(s3Region, visited, "s3-region", cfg.S3.Region)
	applyStringDefault(sftpUser, visited, "sftp-user", cfg.SFTP.User)
	applyStringDefault(sftpPassword, visited, "sftp-pass", cfg.SFTP.Password)
	applyStringDefault(sftpKey, visited, "sftp-key", cfg.SFTP.KeyPath)
	applyStringDefault(sftpKeyPassphrase, visited, "sftp-key-pass", cfg.SFTP.KeyPassphrase)
	applyStringDefault(sftpKnownHosts, visited, "sftp-known-hosts", cfg.SFTP.KnownHosts)
	applyBoolDefault(sftpInsecure, visited, "sftp-insecure", cfg.SFTP.Insecure)

	headerLines := []string(cliHeaders)
	if !visited["H"] && len(cfg.Headers) > 0 {
		headerLines = cfg.Headers
	}
	headers, err := headersFromLines(headerLines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}
	mirrors := parseMirrorFlag(*mirror)
	if !visited["mirror"] && len(cfg.Mirrors) > 0 {
		mirrors = cfg.Mirrors
	}

	appSourceOptions = sourceOptions{
		s3: s3Options{
			Endpoint:     *s3Endpoint,
			AccessKey:    *s3AccessKey,
			SecretKey:    *s3SecretKey,
			SessionToken: *s3SessionToken,
			Region:       *s3Region,
		},
		sftp: sftpOptions{
			User:           *sftpUser,
			Password:       *sftpPassword,
			KeyPath:        *sftpKey,
			KeyPassphrase:  *sftpKeyPassphrase,
			KnownHostsPath: *sftpKnownHosts,
			Insecure:       *sftpInsecure,
		},
		headers: headers,
		mirrors: mirrors,
	}

	urls, err := collectURLs(flag.Args(), *listPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}
	if len(urls) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *expectedSHA256 != "" && len(urls) > 1 {
		fmt.Fprintln(os.Stderr, "参数错误: -sha256 仅支持单个下载任务")
		os.Exit(2)
	}

	expectedHash := ""
	if *expectedSHA256 != "" {
		expectedHash, err = normalizeSHA256(*expectedSHA256)
		if err != nil {
			fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
			os.Exit(2)
		}
	}

	if !visited["o"] && cfg.DownloadDir != "" {
		if len(urls) == 1 {
			if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "创建配置下载目录失败: %v\n", err)
				os.Exit(2)
			}
			*output = filepath.Join(cfg.DownloadDir, defaultOutputName(urls[0]))
		} else {
			*output = cfg.DownloadDir
		}
	}

	tasks, err := buildTasks(urls, *output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}

	// 初始化全局限速器和显式代理。
	InitLimiter(*limit)
	if err := InitProxy(*proxy); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}

	// 监听 Ctrl+C 和终止信号。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(tasks) == 1 {
		if err := downloadOne(ctx, tasks[0], expectedHash); err != nil {
			exitDownloadError(err)
		}
		return
	}

	results := runBatch(ctx, tasks)
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
		os.Exit(130)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// downloadOne 创建任务对应的数据源并执行单个下载。
func downloadOne(ctx context.Context, task downloadTask, expectedHash string) error {
	src, err := newSource(task.URL)
	if err != nil {
		return err
	}
	defer closeSource(src)

	return downloadSource(ctx, src, task.OutputPath, expectedHash)
}

// downloadSource 执行单个数据源下载，并按需进行 SHA256 校验。
func downloadSource(ctx context.Context, src Source, outputPath, expectedHash string) error {
	if err := Download(ctx, src, outputPath); err != nil {
		return err
	}

	if expectedHash != "" {
		actual, err := VerifySHA256(outputPath, expectedHash)
		if err != nil {
			return err
		}
		fmt.Printf("SHA256 校验通过 (%s): %s\n", outputPath, actual)
	}

	// 仅在完整下载且校验通过后删除状态文件。
	if err := os.Remove(StatePath(outputPath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除状态文件失败: %w", err)
	}
	return nil
}

// runSourceCLI 为子命令创建信号上下文并执行下载。
func runSourceCLI(src Source, outputPath string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := downloadSource(ctx, src, outputPath, "")
	closeSource(src)
	if err != nil {
		exitDownloadError(err)
	}
}

// exitDownloadError 根据错误类型输出信息并退出。
func exitDownloadError(err error) {
	if errors.Is(err, context.Canceled) {
		fmt.Println("下载已取消，可使用相同命令继续下载。")
		os.Exit(130)
	}
	fmt.Fprintf(os.Stderr, "下载失败: %v\n", err)
	os.Exit(1)
}

// defaultOutputName 根据 URL 路径推导安全的默认输出文件名。
func defaultOutputName(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return defaultSourceOutputName(u.Path)
	}
	return "download.bin"
}

// reorderArgs 将选项移动到位置参数之前，使选项可以出现在 URL 前后。
func reorderArgs(args []string) []string {
	var flags []string
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if name == "" || strings.Contains(name, "=") {
			continue
		}

		defined := flag.CommandLine.Lookup(name)
		if defined == nil {
			// 未知选项交给 flag.Parse 统一报错，避免误吞位置参数。
			continue
		}
		if boolFlag, ok := defined.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positional...)
}

// parsePullArgs 解析 "pull <位置参数> [选项]" 形式的参数，允许选项在前或在后。
func parsePullArgs(fs *flag.FlagSet, args []string, positionalName string) (string, error) {
	var positional string
	var flagArgs []string

	appendPositional := func(value string) error {
		if positional != "" {
			return fmt.Errorf("只能指定一个%s", positionalName)
		}
		positional = value
		return nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			for _, value := range args[i+1:] {
				if err := appendPositional(value); err != nil {
					return "", err
				}
			}
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if err := appendPositional(arg); err != nil {
				return "", err
			}
			continue
		}

		flagArgs = append(flagArgs, arg)
		name := strings.TrimLeft(arg, "-")
		if name == "" || strings.Contains(name, "=") {
			continue
		}

		defined := fs.Lookup(name)
		if defined == nil {
			continue
		}
		if boolFlag, ok := defined.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}

	if err := fs.Parse(flagArgs); err != nil {
		return "", err
	}
	if positional == "" {
		return "", fmt.Errorf("缺少%s", positionalName)
	}
	return positional, nil
}
