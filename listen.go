package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const listenQueueSize = 128

// listenOptions 是 listen 子命令的配置。
type listenOptions struct {
	Host      string
	Port      int
	OutputDir string
	LimitKBps int
	Proxy     string
}

// downloadAPIRequest 是 POST /api/download 的请求体。
type downloadAPIRequest struct {
	URL    string `json:"url"`
	Output string `json:"output,omitempty"`
}

// listenTask 是监听服务中的一个下载任务。
type listenTask struct {
	ID         int        `json:"id"`
	URL        string     `json:"url"`
	Output     string     `json:"output"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// listenServer 维护后台下载队列和任务状态。
type listenServer struct {
	ctx           context.Context
	cancel        context.CancelFunc
	options       listenOptions
	sourceOptions sourceOptions
	jobs          chan *listenTask

	tasksMu sync.Mutex
	tasks   []*listenTask
	nextID  int

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// listenCLI 处理 listen 子命令。
func listenCLI(args []string) {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	host := fs.String("host", "127.0.0.1", "监听地址（默认仅本机）")
	port := fs.Int("port", 16800, "监听端口")
	output := fs.String("o", "", "下载目录（默认使用配置 download_dir 或当前目录）")
	limit := fs.Int("limit", 0, "限速（KB/s，0 表示不限速）")
	proxy := fs.String("proxy", "", "代理地址（支持 http:// 和 socks5://）")
	mirror := fs.String("mirror", "", "镜像模板，逗号分隔，例如 https://ghproxy.com/{url}")
	var cliHeaders headerList
	fs.Var(&cliHeaders, "H", "自定义请求头，可多次指定，例如 -H \"Cookie: xxx\"")

	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "参数错误: listen 不接受位置参数: %s\n", strings.Join(fs.Args(), " "))
		listenUsage()
		os.Exit(2)
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(2)
	}
	visited := visitedFlags(fs)
	applyStringDefault(host, visited, "host", cfg.Listen.Host)
	applyIntDefault(port, visited, "port", cfg.Listen.Port)
	applyIntDefault(limit, visited, "limit", cfg.Limit)
	applyStringDefault(proxy, visited, "proxy", cfg.Proxy)
	if !visited["o"] && cfg.DownloadDir != "" {
		*output = cfg.DownloadDir
	}
	if *output == "" {
		*output = "."
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "创建下载目录失败: %v\n", err)
		os.Exit(2)
	}

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

	InitLimiter(*limit)
	if err := InitProxy(*proxy); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	options := listenOptions{
		Host:      *host,
		Port:      *port,
		OutputDir: *output,
		LimitKBps: *limit,
		Proxy:     *proxy,
	}
	srcOptions := sourceOptions{
		s3:      cfg.configS3Options(),
		sftp:    cfg.configSFTPOptions(),
		headers: headers,
		mirrors: mirrors,
	}

	if err := runListen(ctx, options, srcOptions); err != nil {
		fmt.Fprintf(os.Stderr, "[listen] 服务异常退出: %v\n", err)
		os.Exit(1)
	}
}

func listenUsage() {
	fmt.Fprintln(os.Stderr, "用法: lift listen [-host 127.0.0.1] [-port 16800] [-o 下载目录] [-limit KB/s] [-proxy URL] [-mirror 模板] [-H \"Name: value\"]")
}

// runListen 启动 HTTP API 服务，等待退出信号后优雅关闭。
func runListen(ctx context.Context, options listenOptions, srcOptions sourceOptions) error {
	address := net.JoinHostPort(options.Host, strconv.Itoa(options.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", address, err)
	}

	server := newListenServer(ctx, options, srcOptions)
	httpServer := &http.Server{
		Handler:           server.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("[listen] 服务已启动: http://%s\n", listener.Addr().String())
	fmt.Printf("[listen] 下载目录: %s\n", options.OutputDir)
	fmt.Println("[listen] POST /api/download  {\"url\":\"https://example.com/file.bin\"}")
	fmt.Println("[listen] GET  /api/tasks     查询任务状态")
	fmt.Println("[listen] GET  /api/health    健康检查")

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		fmt.Println("\n[listen] 收到退出信号，正在关闭服务...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = httpServer.Shutdown(shutdownCtx)
		cancel()
		server.Close()
		return nil
	case err := <-errCh:
		_ = httpServer.Close()
		server.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// newListenServer 创建任务队列并启动固定数量的下载 worker。
func newListenServer(ctx context.Context, options listenOptions, srcOptions sourceOptions) *listenServer {
	serverCtx, cancel := context.WithCancel(ctx)
	server := &listenServer{
		ctx:           serverCtx,
		cancel:        cancel,
		options:       options,
		sourceOptions: srcOptions,
		jobs:          make(chan *listenTask, listenQueueSize),
	}
	for i := 0; i < maxConcurrentFiles; i++ {
		server.wg.Add(1)
		go server.worker()
	}
	return server
}

// handler 构造 API 路由。
func (s *listenServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/", s.handleTaskByID)
	mux.HandleFunc("/api/health", s.handleHealth)
	return mux
}

func (s *listenServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request downloadAPIRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("解析 JSON 失败: %v", err)})
		return
	}
	if strings.TrimSpace(request.URL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 url 字段"})
		return
	}

	task, err := s.enqueue(request)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *listenServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 GET"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": s.snapshotTasks()})
}

func (s *listenServer) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 GET"})
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/tasks/"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "任务 ID 无效"})
		return
	}

	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	for _, task := range s.tasks {
		if task.ID == id {
			writeJSON(w, http.StatusOK, *task)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
}

func (s *listenServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 GET"})
		return
	}
	s.tasksMu.Lock()
	queued := 0
	running := 0
	for _, task := range s.tasks {
		switch task.Status {
		case "queued":
			queued++
		case "running":
			running++
		}
	}
	total := len(s.tasks)
	s.tasksMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"queued":  queued,
		"running": running,
		"total":   total,
	})
}

// enqueue 校验请求并把任务放入后台队列。
func (s *listenServer) enqueue(request downloadAPIRequest) (listenTask, error) {
	if err := s.ctx.Err(); err != nil {
		return listenTask{}, fmt.Errorf("服务正在关闭")
	}

	rawURL := strings.TrimSpace(request.URL)
	probe, err := newSourceWithOptions(rawURL, s.sourceOptions)
	if err != nil {
		return listenTask{}, err
	}
	closeSource(probe)

	output, err := s.reserveOutput(rawURL, strings.TrimSpace(request.Output))
	if err != nil {
		return listenTask{}, err
	}

	s.tasksMu.Lock()
	s.nextID++
	task := &listenTask{
		ID:        s.nextID,
		URL:       rawURL,
		Output:    output,
		Status:    "queued",
		CreatedAt: time.Now(),
	}
	s.tasks = append(s.tasks, task)
	snapshot := *task
	s.tasksMu.Unlock()

	select {
	case s.jobs <- task:
		batchLogf("[listen] 接收任务 #%d: %s -> %s\n", task.ID, rawURL, output)
		return snapshot, nil
	case <-s.ctx.Done():
		s.finishTask(task, "failed", s.ctx.Err())
		return listenTask{}, fmt.Errorf("服务正在关闭")
	default:
		s.finishTask(task, "failed", errors.New("任务队列已满"))
		return listenTask{}, fmt.Errorf("任务队列已满")
	}
}

// reserveOutput 生成不会覆盖现有文件的唯一输出路径。
func (s *listenServer) reserveOutput(rawURL, requested string) (string, error) {
	baseDir := s.options.OutputDir
	if baseDir == "" {
		baseDir = "."
	}

	var candidate string
	switch {
	case requested == "":
		candidate = filepath.Join(baseDir, safeBaseName(defaultOutputName(rawURL)))
	case filepath.IsAbs(requested):
		candidate = requested
	default:
		candidate = filepath.Join(baseDir, safeBaseName(requested))
	}
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		return "", fmt.Errorf("创建输出目录失败: %w", err)
	}

	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	final := candidate
	ext := filepath.Ext(candidate)
	base := strings.TrimSuffix(candidate, ext)
	for suffix := 1; s.outputReserved(final); suffix++ {
		final = fmt.Sprintf("%s.%d%s", base, suffix, ext)
	}
	return final, nil
}

// outputReserved 检查输出路径是否已被待执行/进行中的任务或磁盘文件占用。
func (s *listenServer) outputReserved(path string) bool {
	for _, task := range s.tasks {
		if task.Output == path && (task.Status == "queued" || task.Status == "running") {
			return true
		}
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

// worker 从队列取任务并执行下载。
func (s *listenServer) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case task := <-s.jobs:
			if err := s.ctx.Err(); err != nil {
				s.finishTask(task, "failed", err)
				continue
			}

			s.tasksMu.Lock()
			now := time.Now()
			task.Status = "running"
			task.StartedAt = &now
			label := fmt.Sprintf("#%d", task.ID)
			s.tasksMu.Unlock()
			batchLogf("[listen] 开始任务 %s: %s\n", label, task.Output)

			err := s.downloadTask(task)
			switch {
			case err == nil:
				s.finishTask(task, "success", nil)
				batchLogf("[listen] 完成任务 %s: %s\n", label, task.Output)
			case errors.Is(err, context.Canceled):
				s.finishTask(task, "canceled", err)
				batchLogf("[listen] 取消任务 %s: %s\n", label, task.Output)
			default:
				s.finishTask(task, "failed", err)
				batchLogf("[listen] 任务失败 %s: %s | %v\n", label, task.Output, err)
			}
		}
	}
}

// downloadTask 使用现有下载引擎执行单个后台任务。
func (s *listenServer) downloadTask(task *listenTask) error {
	src, err := newSourceWithOptions(task.URL, s.sourceOptions)
	if err != nil {
		return err
	}
	defer closeSource(src)

	err = download(s.ctx, src, task.Output, filepath.Base(task.Output), true)
	if err == nil {
		if removeErr := os.Remove(StatePath(task.Output)); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("删除状态文件失败: %w", removeErr)
		}
	}
	return err
}

// finishTask 更新任务最终状态。
func (s *listenServer) finishTask(task *listenTask, status string, err error) {
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	now := time.Now()
	task.Status = status
	task.FinishedAt = &now
	if err != nil {
		task.Error = err.Error()
	}
}

// snapshotTasks 返回任务列表副本。
func (s *listenServer) snapshotTasks() []listenTask {
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	result := make([]listenTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		result = append(result, *task)
	}
	return result
}

// Close 停止接收新任务并等待 worker 退出。
func (s *listenServer) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()

		s.tasksMu.Lock()
		defer s.tasksMu.Unlock()
		now := time.Now()
		for _, task := range s.tasks {
			if task.Status == "queued" {
				task.Status = "canceled"
				task.Error = "服务已关闭"
				task.FinishedAt = &now
			}
		}
	})
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
