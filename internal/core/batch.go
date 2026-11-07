package core

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

// MaxConcurrentFiles 是批量下载器和 HTTP API 服务允许同时下载的最大文件数。
const MaxConcurrentFiles = 2

// BatchTask 是文件级下载任务。
type BatchTask struct {
	URL        string
	OutputPath string
}

// BatchResult 是单个批量任务的结果。
type BatchResult struct {
	Task     BatchTask
	Err      error
	Duration time.Duration
}

// SourceFactory 为 URL 创建下载源。
type SourceFactory func(rawURL string) (Source, error)

// CollectURLs 合并命令行 URL 和列表文件条目，忽略空行和 # 注释。
func CollectURLs(args []string, listPath string) ([]string, error) {
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

// BuildTasks 规划输出路径。批量模式下 output 视为目录。
func BuildTasks(urls []string, output string) ([]BatchTask, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("没有可下载的 URL")
	}

	if len(urls) == 1 {
		outputPath := output
		if outputPath == "" {
			outputPath = DefaultOutputName(urls[0])
		}
		return []BatchTask{{URL: urls[0], OutputPath: outputPath}}, nil
	}

	outputDir := output
	if outputDir == "" {
		outputDir = "."
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}

	tasks := make([]BatchTask, 0, len(urls))
	seen := make(map[string]string, len(urls))
	for _, rawURL := range urls {
		outputPath := filepath.Join(outputDir, SafeBaseName(DefaultOutputName(rawURL)))
		if previous, exists := seen[outputPath]; exists {
			return nil, fmt.Errorf("多个 URL 对应同一输出文件 %s（%s 与 %s）", outputPath, previous, rawURL)
		}
		seen[outputPath] = rawURL
		tasks = append(tasks, BatchTask{URL: rawURL, OutputPath: outputPath})
	}
	return tasks, nil
}

// RunBatch 使用有界协程池执行任务。
func RunBatch(ctx context.Context, tasks []BatchTask, newSource SourceFactory) []BatchResult {
	results := make([]BatchResult, len(tasks))
	jobs := make(chan int)

	workerTotal := MaxConcurrentFiles
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
					results[index] = BatchResult{Task: task, Err: err}
					continue
				}

				started := time.Now()
				Logf("[批量] 开始下载 (%d/%d): %s\n", index+1, len(tasks), task.OutputPath)
				err := executeDownloadTask(ctx, task, newSource)

				duration := time.Since(started)
				results[index] = BatchResult{Task: task, Err: err, Duration: duration}
				if err != nil {
					Logf("[批量] 失败 (%d/%d): %s | %v | 用时 %s\n",
						index+1, len(tasks), task.OutputPath, err, duration.Round(time.Millisecond))
				} else {
					Logf("[批量] 完成 (%d/%d): %s | 用时 %s\n",
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
				results[pending] = BatchResult{Task: tasks[pending], Err: ctx.Err()}
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

func executeDownloadTask(ctx context.Context, task BatchTask, newSource SourceFactory) error {
	src, err := newSource(task.URL)
	if err != nil {
		return err
	}
	defer CloseSource(src)

	err = DownloadTo(ctx, src, task.OutputPath, filepath.Base(task.OutputPath), true)
	if err == nil {
		if removeErr := os.Remove(StatePath(task.OutputPath)); removeErr != nil && !os.IsNotExist(removeErr) {
			err = fmt.Errorf("删除状态文件失败: %w", removeErr)
		}
	}
	return err
}
