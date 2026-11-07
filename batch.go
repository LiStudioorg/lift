package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxConcurrentFiles = 2

// downloadTask 表示一个文件级下载任务。
type downloadTask struct {
	URL        string
	OutputPath string
}

// batchResult 表示单个任务的执行结果。
type batchResult struct {
	Task     downloadTask
	Err      error
	Duration time.Duration
}

// collectURLs 合并命令行 URL 和列表文件中的 URL，忽略空行与 # 注释。
func collectURLs(args []string, listPath string) ([]string, error) {
	var urls []string
	for _, arg := range args {
		if value := strings.TrimSpace(arg); value != "" {
			urls = append(urls, value)
		}
	}

	if listPath == "" {
		return urls, nil
	}

	f, err := os.Open(listPath)
	if err != nil {
		return nil, fmt.Errorf("打开 URL 列表文件失败: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 URL 列表文件失败: %w", err)
	}
	return urls, nil
}

// buildTasks 为每个 URL 规划输出路径。批量下载时 -o 作为输出目录。
func buildTasks(urls []string, output string) ([]downloadTask, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("没有可下载的 URL")
	}

	if len(urls) == 1 {
		outputPath := output
		if outputPath == "" {
			outputPath = defaultOutputName(urls[0])
		}
		return []downloadTask{{URL: urls[0], OutputPath: outputPath}}, nil
	}

	outputDir := output
	if outputDir == "" {
		outputDir = "."
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}

	tasks := make([]downloadTask, 0, len(urls))
	seen := make(map[string]string, len(urls))
	for _, rawURL := range urls {
		outputPath := filepath.Join(outputDir, safeBaseName(defaultOutputName(rawURL)))
		if previous, exists := seen[outputPath]; exists {
			return nil, fmt.Errorf("多个 URL 对应同一输出文件 %s（%s 与 %s）", outputPath, previous, rawURL)
		}
		seen[outputPath] = rawURL
		tasks = append(tasks, downloadTask{URL: rawURL, OutputPath: outputPath})
	}
	return tasks, nil
}

// safeBaseName 仅保留文件名部分，防止 URL 中的 ../ 造成目录穿越。
func safeBaseName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "download.bin"
	}
	return name
}

// runBatch 使用固定数量 worker 执行任务队列，保证最多 maxConcurrentFiles 个文件并发下载。
func runBatch(ctx context.Context, tasks []downloadTask) []batchResult {
	results := make([]batchResult, len(tasks))
	jobs := make(chan int)

	workerTotal := maxConcurrentFiles
	if len(tasks) < workerTotal {
		workerTotal = len(tasks)
	}

	var wg sync.WaitGroup
	wg.Add(workerTotal)
	for i := 0; i < workerTotal; i++ {
		go func() {
			defer wg.Done()
			for index := range jobs {
				task := tasks[index]
				if err := ctx.Err(); err != nil {
					results[index] = batchResult{Task: task, Err: err}
					continue
				}

				started := time.Now()
				batchLogf("[批量] 开始下载 (%d/%d): %s\n", index+1, len(tasks), task.OutputPath)
				err := executeDownloadTask(ctx, task)

				duration := time.Since(started)
				results[index] = batchResult{Task: task, Err: err, Duration: duration}
				if err != nil {
					batchLogf("[批量] 失败 (%d/%d): %s | %v | 用时 %s\n",
						index+1, len(tasks), task.OutputPath, err, duration.Round(time.Millisecond))
				} else {
					batchLogf("[批量] 完成 (%d/%d): %s | 用时 %s\n",
						index+1, len(tasks), task.OutputPath, duration.Round(time.Millisecond))
				}
			}
		}()
	}

	for index := range tasks {
		select {
		case jobs <- index:
		case <-ctx.Done():
			for pending := index; pending < len(tasks); pending++ {
				results[pending] = batchResult{Task: tasks[pending], Err: ctx.Err()}
			}
			close(jobs)
			wg.Wait()
			return results
		}
	}

	close(jobs)
	wg.Wait()
	return results
}

// executeDownloadTask 创建任务对应的数据源并执行批量下载。
func executeDownloadTask(ctx context.Context, task downloadTask) error {
	src, err := newSource(task.URL)
	if err != nil {
		return err
	}
	defer closeSource(src)

	err = download(ctx, src, task.OutputPath, filepath.Base(task.OutputPath), true)
	if err == nil {
		if removeErr := os.Remove(StatePath(task.OutputPath)); removeErr != nil && !os.IsNotExist(removeErr) {
			err = fmt.Errorf("删除状态文件失败: %w", removeErr)
		}
	}
	return err
}

// batchLogf 串行输出批量下载日志，避免多个文件互相穿插。
func batchLogf(format string, args ...any) {
	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Printf(format, args...)
}
