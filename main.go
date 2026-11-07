package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path"
	"syscall"
)

func main() {
	output := flag.String("o", "", "输出文件名；批量下载时作为输出目录")
	limit := flag.Int("limit", 0, "限速（KB/s，0 表示不限速）")
	listPath := flag.String("f", "", "包含多个 URL 的列表文件（每行一个，忽略空行和 # 注释）")
	expectedSHA256 := flag.String("sha256", "", "下载完成后校验文件的 SHA256（仅支持单个下载任务）")
	proxy := flag.String("proxy", "", "代理地址（支持 http:// 和 socks5://）")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法: %s [选项] <URL> [URL...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

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

// downloadOne 执行单个下载任务，并按需进行 SHA256 校验。
func downloadOne(ctx context.Context, task downloadTask, expectedHash string) error {
	if err := Download(ctx, NewHTTPSource(task.URL), task.OutputPath); err != nil {
		return err
	}

	if expectedHash != "" {
		actual, err := VerifySHA256(task.OutputPath, expectedHash)
		if err != nil {
			return err
		}
		fmt.Printf("SHA256 校验通过 (%s): %s\n", task.OutputPath, actual)
	}

	// 仅在完整下载且校验通过后删除状态文件。
	if err := os.Remove(StatePath(task.OutputPath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除状态文件失败: %w", err)
	}
	return nil
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
		return safeBaseName(path.Base(u.Path))
	}
	return "download.bin"
}
