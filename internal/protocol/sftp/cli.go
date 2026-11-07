package sftp

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

// CLI 实现 "lift sftp pull" 子命令。
func CLI(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		sftpUsage()
		return 0
	}
	if len(args) == 0 || args[0] != "pull" {
		sftpUsage()
		return 2
	}

	fs := flag.NewFlagSet("sftp pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = sftpUsage
	host := fs.String("host", "", "SFTP 主机（可含端口）")
	port := fs.Int("port", 0, "SFTP 端口（主机未包含端口时使用，默认 22）")
	user := fs.String("user", "", "SFTP 用户名")
	password := fs.String("pass", "", "SFTP 密码")
	keyPath := fs.String("key", "", "SFTP 私钥路径")
	keyPassphrase := fs.String("key-pass", "", "SFTP 私钥密码")
	knownHosts := fs.String("known-hosts", "", "known_hosts 路径")
	insecure := fs.Bool("insecure", false, "跳过主机密钥校验（不推荐）")
	output := fs.String("o", "", "输出文件名")
	limit := fs.Int("limit", 0, "限速（KB/s，0 表示不限速）")

	remotePath, err := cli.ParsePullArgs(fs, args[1:], "远端路径")
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		sftpUsage()
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}
	visited := config.VisitedFlags(fs)
	config.ApplyStringDefault(host, visited, "host", cfg.SFTP.Host)
	config.ApplyIntDefault(port, visited, "port", cfg.SFTP.Port)
	config.ApplyStringDefault(user, visited, "user", cfg.SFTP.User)
	config.ApplyStringDefault(password, visited, "pass", cfg.SFTP.Password)
	config.ApplyStringDefault(keyPath, visited, "key", cfg.SFTP.KeyPath)
	config.ApplyStringDefault(keyPassphrase, visited, "key-pass", cfg.SFTP.KeyPassphrase)
	config.ApplyStringDefault(knownHosts, visited, "known-hosts", cfg.SFTP.KnownHosts)
	config.ApplyBoolDefault(insecure, visited, "insecure", cfg.SFTP.Insecure)
	config.ApplyIntDefault(limit, visited, "limit", cfg.Limit)

	if *host == "" {
		fmt.Fprintln(os.Stderr, "参数错误: 缺少 --host")
		sftpUsage()
		return 2
	}
	defaultName := core.DefaultSourceOutputName(remotePath)
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

	src, err := New(Options{
		Host:           *host,
		Port:           *port,
		User:           *user,
		Password:       *password,
		KeyPath:        *keyPath,
		KeyPassphrase:  *keyPassphrase,
		KnownHostsPath: *knownHosts,
		Insecure:       *insecure,
	}, remotePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli.RunSource(ctx, src, *output, "")
}

func sftpUsage() {
	fmt.Fprintln(os.Stderr, "用法: lift sftp pull <远端路径> --host <主机[:端口]> --user <用户名> (--pass <密码> | --key <私钥路径>) [-o 输出文件] [-limit KB/s]")
}
