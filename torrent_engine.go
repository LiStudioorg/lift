package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"golang.org/x/time/rate"
)

// magnetOptions 是 magnet 子命令的参数。
type magnetOptions struct {
	URI        string
	OutputDir  string
	ListenPort int
	NoDHT      bool
}

// magnetCLI 处理 magnet 子命令。
func magnetCLI(args []string) {
	fs := flag.NewFlagSet("magnet", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	output := fs.String("o", ".", "输出目录")
	limit := fs.Int("limit", 0, "限速（KB/s，0 表示不限速）")
	listenPort := fs.Int("port", 0, "BT 监听端口（0 表示随机端口）")
	noDHT := fs.Bool("no-dht", false, "禁用 DHT，仅使用磁力链中的 Tracker")

	uri, err := parsePullArgs(fs, args, "磁力链接")
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		magnetUsage()
		os.Exit(2)
	}
	if !strings.HasPrefix(strings.ToLower(uri), "magnet:?") {
		fmt.Fprintf(os.Stderr, "参数错误: 无效的磁力链接（必须以 magnet:? 开头）\n")
		magnetUsage()
		os.Exit(2)
	}

	InitLimiter(*limit)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = runMagnet(ctx, magnetOptions{
		URI:        uri,
		OutputDir:  *output,
		ListenPort: *listenPort,
		NoDHT:      *noDHT,
	})
	if errors.Is(err, context.Canceled) {
		fmt.Println("\n[BT] 下载已中断，已完成的 Piece 已保留，可重新运行同一磁力链继续下载。")
		os.Exit(130)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "磁力链下载失败: %v\n", err)
		os.Exit(1)
	}
}

func magnetUsage() {
	fmt.Fprintln(os.Stderr, "用法: lift magnet <magnet_uri> -o <output_dir> [-limit KB/s] [-no-dht] [-port 0]")
}

// runMagnet 启动独立的 BitTorrent 引擎并等待下载完成或退出信号。
func runMagnet(ctx context.Context, options magnetOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.OutputDir == "" {
		options.OutputDir = "."
	}
	if err := os.MkdirAll(options.OutputDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	absOutputDir, err := filepath.Abs(options.OutputDir)
	if err != nil {
		return fmt.Errorf("解析输出目录失败: %w", err)
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = absOutputDir
	cfg.ListenPort = options.ListenPort
	cfg.NoDHT = options.NoDHT
	cfg.NoDefaultPortForwarding = true
	cfg.DownloadRateLimiter = globalLimiter
	if globalLimiter != nil {
		// 上传和下载分别使用独立限速器，避免两个方向争抢同一桶令牌。
		cfg.UploadRateLimiter = rate.NewLimiter(globalLimiter.Limit(), globalLimiter.Burst())
	}

	client, err := torrent.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("初始化 BitTorrent 客户端失败: %w", err)
	}
	defer func() {
		if closeErrs := client.Close(); len(closeErrs) > 0 {
			fmt.Fprintf(os.Stderr, "[BT] 关闭客户端时出现警告: %v\n", closeErrs)
		}
	}()

	t, err := client.AddMagnet(options.URI)
	if err != nil {
		return fmt.Errorf("解析磁力链失败: %w", err)
	}

	fmt.Printf("[BT] 磁力链: %s\n", shortMagnet(options.URI))
	fmt.Printf("[BT] 输出目录: %s\n", absOutputDir)
	fmt.Printf("[BT] 等待元数据...\n")

	gotInfo := t.GotInfo()
	metadataReady := false
	startedAt := time.Now()
	var lastTime time.Time
	var lastBytes int64

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n[BT] 收到退出信号，正在安全关闭 BT 引擎...")
			return ctx.Err()

		case <-gotInfo:
			gotInfo = nil
			metadataReady = true
			t.DownloadAll()

			info := t.Info()
			name := t.Name()
			var total int64
			var pieces int
			if info != nil {
				name = info.BestName()
				total = info.TotalLength()
				pieces = info.NumPieces()
			}
			fmt.Printf("\n[BT] 元数据获取成功: %s | 总大小: %s | Piece 数: %d\n",
				name, formatBytes(total), pieces)

			lastTime = time.Now()
			lastBytes = t.BytesCompleted()

		case now := <-ticker.C:
			stats := t.Stats()
			downloaded := t.BytesCompleted()
			speed := 0.0
			if !lastTime.IsZero() {
				elapsed := now.Sub(lastTime).Seconds()
				if elapsed > 0 {
					speed = float64(downloaded-lastBytes) / elapsed
				}
			}

			if !metadataReady {
				printTorrentProgress(fmt.Sprintf("等待元数据 | Peers: %d | 已连接: %d | 种子: %d",
					stats.TotalPeers, stats.ActivePeers, stats.ConnectedSeeders))
				lastTime = now
				lastBytes = downloaded
				continue
			}

			total := t.Length()
			percent := 0.0
			if total > 0 {
				percent = float64(downloaded) / float64(total) * 100
			}
			eta := "--:--"
			if total > 0 && downloaded >= total {
				eta = "00:00"
			} else if speed >= 1 {
				eta = formatETA(float64(total-downloaded) / speed)
			}

			printTorrentProgress(fmt.Sprintf("进度: %5.1f%% | 已下载: %s/%s | Peers: %d | 种子: %d | 速度: %.2fMB/s | 剩余: %s",
				percent,
				formatBytes(downloaded),
				formatBytes(total),
				stats.ActivePeers,
				stats.ConnectedSeeders,
				speed/(1024*1024),
				eta))

			lastTime = now
			lastBytes = downloaded

			// Complete() 标志存在异步延迟，直接以缺失字节数判断更可靠。
			if t.BytesMissing() == 0 {
				fmt.Println()
				fmt.Printf("[BT] 下载完成: %s | 用时: %s\n", absOutputDir, time.Since(startedAt).Round(time.Second))
				return nil
			}
		}
	}
}

// printTorrentProgress 使用回车刷新单行进度。
func printTorrentProgress(line string) {
	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Printf("\r[BT] %s", line)
}

// shortMagnet 截断过长的磁力链，避免刷屏。
func shortMagnet(uri string) string {
	const maxLength = 120
	if len(uri) <= maxLength {
		return uri
	}
	return uri[:maxLength] + "..."
}

// formatBytes 将字节数格式化为便于阅读的单位。
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
