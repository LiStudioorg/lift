package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// chunkSize 是每个分片的大小（4MB）。
	chunkSize = 4 * 1024 * 1024
	// workerCount 是单文件并发下载的协程数量。
	workerCount = 8
	// readBufSize 是单次读取下载流的缓冲区大小。
	readBufSize = 32 * 1024
	// maxRetries 是单个分片的最大重试次数。
	maxRetries = 3
)

var (
	// retryDelays 是每次重试前的等待时间，依次为 1s、2s、4s。
	retryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	// outputMu 防止多个下载任务的进度和日志互相穿插。
	outputMu sync.Mutex
)

// urlSource 是可选接口，用于获取下载源地址并写入状态文件。
type urlSource interface {
	URL() string
}

// chunkJob 表示一个待下载的分片任务。
type chunkJob struct {
	index int   // 分片在状态中的下标
	start int64 // 本次下载的起始偏移量
	end   int64 // 本次下载的结束偏移量（不包含）
}

// Download 下载 src 指向的对象到 outputPath，并支持断点续传。
func Download(ctx context.Context, src Source, outputPath string) error {
	return download(ctx, src, outputPath, "", false)
}

// download 是 Download 的内部实现。batch 为 true 时使用带文件名的多行进度输出。
func download(ctx context.Context, src Source, outputPath, label string, batch bool) error {
	if label == "" {
		label = filepath.Base(outputPath)
	}

	logf := func(format string, args ...any) {
		outputMu.Lock()
		defer outputMu.Unlock()
		fmt.Printf(format, args...)
	}
	logErr := func(format string, args ...any) {
		outputMu.Lock()
		defer outputMu.Unlock()
		fmt.Fprintf(os.Stderr, format, args...)
	}

	info, err := src.Stat(ctx)
	if err != nil {
		return fmt.Errorf("获取远端文件信息失败: %w", err)
	}
	if info.Size < 0 {
		return fmt.Errorf("远端文件大小无效: %d", info.Size)
	}

	sourceURL := ""
	if u, ok := src.(urlSource); ok {
		sourceURL = u.URL()
	}

	statePath := StatePath(outputPath)
	state, loadErr := LoadState(statePath)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		// 状态文件损坏时重新开始，不中断下载。
		logErr("[%s] 读取状态文件失败，将重新开始下载: %v\n", label, loadErr)
		state = nil
	}

	resuming := state != nil &&
		state.ETag == info.ETag &&
		state.Size == info.Size &&
		chunksValid(state.Chunks, info.Size)
	if resuming {
		if batch {
			logf("[%s] 检测到有效断点，继续下载: %s\n", label, outputPath)
		} else {
			logf("检测到有效断点，继续下载: %s\n", outputPath)
		}
	} else {
		state = newDownloadState(sourceURL, info)
	}

	f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("打开输出文件失败: %w", err)
	}
	defer f.Close()

	if err := f.Truncate(info.Size); err != nil {
		return fmt.Errorf("设置输出文件大小失败: %w", err)
	}

	// 跳过已完成分片，并把未完成分片的已下载偏移量加入待下载队列。
	var jobsList []chunkJob
	var completed int64
	for i := range state.Chunks {
		chunk := &state.Chunks[i]
		length := chunk.End - chunk.Start
		if chunk.Downloaded >= length {
			completed += length
			continue
		}
		if chunk.Downloaded < 0 {
			chunk.Downloaded = 0
		}
		completed += chunk.Downloaded
		jobsList = append(jobsList, chunkJob{
			index: i,
			start: chunk.Start + chunk.Downloaded,
			end:   chunk.End,
		})
	}

	var downloaded atomic.Int64
	downloaded.Store(completed)

	if len(jobsList) == 0 {
		if err := SaveState(statePath, state); err != nil {
			return err
		}
		if batch {
			logf("[%s] 文件已完整下载: %s (%d 字节)\n", label, outputPath, info.Size)
		} else {
			logf("文件已完整下载: %s (%d 字节)\n", outputPath, info.Size)
		}
		return nil
	}

	jobs := make(chan chunkJob)
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	setErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		cancelWorkers()
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for job := range jobs {
				err := downloadChunk(workerCtx, src, f, &state.Chunks[job.index], job.start, job.end, &downloaded, label)
				if err == nil {
					continue
				}
				// 因其他协程取消而返回的错误不需要覆盖最初的真实错误。
				if workerCtx.Err() == nil || ctx.Err() == nil {
					setErr(err)
				}
				return
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, job := range jobsList {
			select {
			case jobs <- job:
			case <-workerCtx.Done():
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastTime := time.Now()
	lastBytes := downloaded.Load()

	for {
		select {
		case <-done:
			cancelWorkers()

			errMu.Lock()
			err := firstErr
			errMu.Unlock()

			if syncErr := f.Sync(); syncErr != nil && err == nil {
				err = fmt.Errorf("同步输出文件失败: %w", syncErr)
			}
			if saveErr := SaveState(statePath, state); saveErr != nil && err == nil {
				err = saveErr
			}
			if err != nil {
				return err
			}
			if batch {
				logf("[%s] 下载完成: %s (%d 字节)\n", label, outputPath, info.Size)
			} else {
				logf("\n下载完成: %s (%d 字节)\n", outputPath, info.Size)
			}
			return nil

		case <-ctx.Done():
			cancelWorkers()
			<-done

			if syncErr := f.Sync(); syncErr != nil {
				logErr("[%s] 同步输出文件失败: %v\n", label, syncErr)
			}
			if saveErr := SaveState(statePath, state); saveErr != nil {
				return fmt.Errorf("%w（保存断点失败: %v）", ctx.Err(), saveErr)
			}
			if batch {
				logf("[%s] 下载已中断，断点已保存: %s\n", label, statePath)
			} else {
				logf("\n下载已中断，断点已保存: %s\n", statePath)
			}
			return ctx.Err()

		case <-ticker.C:
			now := time.Now()
			current := downloaded.Load()
			elapsed := now.Sub(lastTime).Seconds()
			speed := 0.0
			if elapsed > 0 {
				speed = float64(current-lastBytes) / elapsed
			}
			percent := 0.0
			if info.Size > 0 {
				percent = float64(current) / float64(info.Size) * 100
			}

			// 计算预计剩余时间；速度过慢或无法计算时显示 --:--。
			remaining := info.Size - current
			eta := "--:--"
			if remaining <= 0 {
				eta = "00:00"
			} else if speed >= 1 {
				eta = formatETA(float64(remaining) / speed)
			}

			outputMu.Lock()
			if batch {
				fmt.Printf("[%s] 进度: %5.1f%% | 已下载: %.1fMB/%.1fMB | 速度: %.1fMB/s | 剩余: %s\n",
					label, percent,
					float64(current)/(1024*1024),
					float64(info.Size)/(1024*1024),
					speed/(1024*1024),
					eta)
			} else {
				fmt.Printf("\r进度: %5.1f%% | 已下载: %.1fMB/%.1fMB | 速度: %.1fMB/s | 剩余: %s",
					percent,
					float64(current)/(1024*1024),
					float64(info.Size)/(1024*1024),
					speed/(1024*1024),
					eta)
			}
			outputMu.Unlock()

			lastTime = now
			lastBytes = current
		}
	}
}

// formatETA 将剩余秒数格式化为 MM:SS 或 HH:MM:SS。
func formatETA(seconds float64) string {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return "--:--"
	}

	total := int64(math.Round(seconds))
	hours := total / 3600
	minutes := (total % 3600) / 60
	secs := total % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

// downloadChunk 下载单个分片，失败时按当前已写入偏移量进行指数退避重试。
func downloadChunk(ctx context.Context, src Source, f *os.File, chunk *Chunk, start, end int64, downloaded *atomic.Int64, label string) error {
	offset := start
	for attempt := 0; ; attempt++ {
		nextOffset, err := downloadChunkOnce(ctx, src, f, chunk, offset, end, downloaded)
		offset = nextOffset
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if attempt >= len(retryDelays) {
			return fmt.Errorf("分片 [%d,%d) 重试 %d 次后仍失败: %w", start, end, len(retryDelays), err)
		}

		delay := retryDelays[attempt]
		logRetry(label, start, end, attempt+1, delay, err)
		if waitErr := waitRetry(ctx, delay); waitErr != nil {
			return waitErr
		}
	}
}

// downloadChunkOnce 执行一次分片下载，并返回下一次可继续写入的偏移量。
func downloadChunkOnce(ctx context.Context, src Source, f *os.File, chunk *Chunk, start, end int64, downloaded *atomic.Int64) (int64, error) {
	body, err := src.OpenStream(ctx, start)
	if err != nil {
		return start, fmt.Errorf("打开下载流失败（偏移 %d）: %w", start, err)
	}
	defer body.Close()

	reader := NewRateLimitedReader(body)
	buf := make([]byte, readBufSize)
	offset := start

	for offset < end {
		want := int64(len(buf))
		if remain := end - offset; remain < want {
			want = remain
		}

		n, readErr := reader.Read(buf[:want])
		if n > 0 {
			// 绝对红线：必须按分片内的绝对偏移量写入，不能使用 file.Write。
			if _, err := f.WriteAt(buf[:n], offset); err != nil {
				return offset, fmt.Errorf("写入文件失败（偏移 %d）: %w", offset, err)
			}
			offset += int64(n)
			chunk.Downloaded = offset - chunk.Start
			downloaded.Add(int64(n))
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if offset < end {
					return offset, fmt.Errorf("下载流提前结束（期望到 %d，实际到 %d）: %w", end, offset, io.ErrUnexpectedEOF)
				}
				break
			}
			return offset, fmt.Errorf("读取下载流失败: %w", readErr)
		}
	}

	if offset != end {
		return offset, fmt.Errorf("分片下载不完整: 期望结束于 %d，实际 %d", end, offset)
	}
	return offset, nil
}

// logRetry 输出分片重试日志。
func logRetry(label string, start, end int64, attempt int, delay time.Duration, err error) {
	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Fprintf(os.Stderr, "\n[%s] 分片 [%d,%d) 失败: %v；%s 后进行第 %d/%d 次重试\n",
		label, start, end, err, delay, attempt, maxRetries)
}

// waitRetry 等待重试间隔，期间响应上下文取消。
func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// chunksValid 检查分片是否连续、完整且覆盖整个文件。
func chunksValid(chunks []Chunk, size int64) bool {
	var offset int64
	for _, chunk := range chunks {
		if chunk.Start != offset || chunk.End < chunk.Start {
			return false
		}
		length := chunk.End - chunk.Start
		if chunk.Downloaded < 0 || chunk.Downloaded > length {
			return false
		}
		offset = chunk.End
	}
	return offset == size
}

// newDownloadState 按 4MB 切分文件并创建新的下载状态。
func newDownloadState(sourceURL string, info *ObjectInfo) *DownloadState {
	state := &DownloadState{
		URL:  sourceURL,
		ETag: info.ETag,
		Size: info.Size,
	}

	for start := int64(0); start < info.Size; start += chunkSize {
		end := start + chunkSize
		if end > info.Size {
			end = info.Size
		}
		state.Chunks = append(state.Chunks, Chunk{
			Start: start,
			End:   end,
		})
	}
	return state
}
