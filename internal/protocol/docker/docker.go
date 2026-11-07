package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/LiStudioorg/lift/internal/config"
	"github.com/LiStudioorg/lift/internal/core"
)

var errDockerHelp = errors.New("help requested")

// dockerPullOptions 是 docker pull 子命令的参数。
type dockerPullOptions struct {
	image     string
	output    string
	limitKBps int
	proxy     string
	outputSet bool
	limitSet  bool
	proxySet  bool
}

// CLI 实现 "lift docker pull" 子命令。
func CLI(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		dockerUsage()
		return 0
	}
	if len(args) == 0 || args[0] != "pull" {
		dockerUsage()
		return 2
	}

	options, err := parseDockerPullArgs(args[1:])
	if err != nil {
		if errors.Is(err, errDockerHelp) {
			dockerUsage()
			return 0
		}
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		dockerUsage()
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		return 2
	}
	if !options.limitSet && cfg.Limit > 0 {
		options.limitKBps = cfg.Limit
	}
	if !options.proxySet && cfg.Proxy != "" {
		options.proxy = cfg.Proxy
	}
	if !options.outputSet && cfg.DownloadDir != "" {
		resolved, resolveErr := config.ResolveOutput(cfg, "", defaultDockerOutputName(options.image))
		if resolveErr != nil {
			fmt.Fprintf(os.Stderr, "输出路径错误: %v\n", resolveErr)
			return 2
		}
		options.output = resolved
	}

	core.InitLimiter(options.limitKBps)
	if err := core.InitProxy(options.proxy); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	core.Logf("[镜像] 开始拉取: %s\n", options.image)
	if err := PullDockerImage(ctx, options.image, options.output); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println("镜像拉取已取消。")
			return 130
		}
		fmt.Fprintf(os.Stderr, "镜像拉取失败: %v\n", err)
		return 1
	}
	return 0
}

// dockerUsage 输出 docker pull 的帮助信息。
func dockerUsage() {
	fmt.Fprintln(os.Stderr, "用法: lift docker pull <镜像名> [-o 输出.tar] [-limit KB/s] [-proxy 代理地址]")
}

// parseDockerPullArgs 解析 docker pull 参数，允许选项出现在镜像名前后。
func parseDockerPullArgs(args []string) (dockerPullOptions, error) {
	var options dockerPullOptions

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return options, errDockerHelp
		case arg == "-o" || arg == "--output":
			i++
			if i >= len(args) || args[i] == "" {
				return options, fmt.Errorf("-o 缺少输出文件名")
			}
			options.output = args[i]
			options.outputSet = true
		case strings.HasPrefix(arg, "-o="):
			options.output = strings.TrimPrefix(arg, "-o=")
			options.outputSet = true
		case strings.HasPrefix(arg, "--output="):
			options.output = strings.TrimPrefix(arg, "--output=")
			options.outputSet = true
		case arg == "-limit" || arg == "--limit":
			i++
			if i >= len(args) {
				return options, fmt.Errorf("-limit 缺少数值")
			}
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return options, fmt.Errorf("-limit 数值无效: %w", err)
			}
			options.limitKBps = value
			options.limitSet = true
		case strings.HasPrefix(arg, "-limit="):
			value, err := strconv.Atoi(strings.TrimPrefix(arg, "-limit="))
			if err != nil {
				return options, fmt.Errorf("-limit 数值无效: %w", err)
			}
			options.limitKBps = value
			options.limitSet = true
		case arg == "-proxy" || arg == "--proxy":
			i++
			if i >= len(args) || args[i] == "" {
				return options, fmt.Errorf("-proxy 缺少代理地址")
			}
			options.proxy = args[i]
			options.proxySet = true
		case strings.HasPrefix(arg, "-proxy="):
			options.proxy = strings.TrimPrefix(arg, "-proxy=")
			options.proxySet = true
		case strings.HasPrefix(arg, "--proxy="):
			options.proxy = strings.TrimPrefix(arg, "--proxy=")
			options.proxySet = true
		case strings.HasPrefix(arg, "-"):
			return options, fmt.Errorf("未知参数: %s", arg)
		default:
			if options.image != "" {
				return options, fmt.Errorf("只能指定一个镜像名")
			}
			options.image = arg
		}
	}

	if options.image == "" {
		return options, fmt.Errorf("缺少镜像名")
	}
	if options.output == "" {
		options.output = defaultDockerOutputName(options.image)
	}
	return options, nil
}

// defaultDockerOutputName 根据镜像名生成默认的 tar 文件名。
func defaultDockerOutputName(image string) string {
	name := strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(image)
	name = core.SafeBaseName(name)
	if name == "" {
		name = "image"
	}
	return name + ".tar"
}

// PullDockerImage 拉取镜像并通过 tarball.Write 保存为 docker load 可导入的 tar。
func PullDockerImage(ctx context.Context, imageName, outputPath string) error {
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return fmt.Errorf("解析镜像引用失败: %w", err)
	}

	progress := newDockerProgress()
	transport := &progressTransport{
		base:     core.HTTPTransport(),
		progress: progress,
	}

	image, err := remote.Image(
		ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithTransport(transport),
	)
	if err != nil {
		return fmt.Errorf("获取远程镜像失败: %w", err)
	}

	layers, err := image.Layers()
	if err != nil {
		return fmt.Errorf("读取镜像层失败: %w", err)
	}
	if err := progress.setLayers(layers); err != nil {
		return err
	}

	digest := ""
	if imageDigest, err := image.Digest(); err == nil {
		digest = imageDigest.String()
	}
	fmt.Printf("[镜像] 摘要: %s | 层数: %d | 压缩大小: %.1fMB\n",
		digest, len(layers), float64(progress.totalSize())/(1024*1024))

	stopProgress := startDockerProgress(progress)
	defer stopProgress()

	tempPath := outputPath + ".tmp"
	f, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("创建输出文件失败: %w", err)
	}

	writeErr := tarball.Write(ref, image, f)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		os.Remove(tempPath)
		return fmt.Errorf("写入镜像 tar 失败: %w", writeErr)
	}
	if closeErr != nil {
		os.Remove(tempPath)
		return fmt.Errorf("关闭镜像 tar 失败: %w", closeErr)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("保存镜像 tar 失败: %w", err)
	}

	stopProgress()
	info, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("读取输出文件信息失败: %w", err)
	}
	fmt.Printf("[镜像] 已保存: %s (%.1fMB)\n", outputPath, float64(info.Size())/(1024*1024))
	progress.printSummary()
	return nil
}

// dockerLayerProgress 记录单个层的下载进度。
type dockerLayerProgress struct {
	index      int
	digest     string
	size       int64
	downloaded int64
	started    bool
	finished   bool
}

// dockerProgress 汇总所有层的进度。
type dockerProgress struct {
	mu     sync.Mutex
	layers map[string]*dockerLayerProgress
	order  []*dockerLayerProgress
	total  int64
	active *dockerLayerProgress
}

func newDockerProgress() *dockerProgress {
	return &dockerProgress{layers: make(map[string]*dockerLayerProgress)}
}

func (p *dockerProgress) setLayers(layers []v1.Layer) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for index, layer := range layers {
		digest, err := layer.Digest()
		if err != nil {
			return fmt.Errorf("读取第 %d 层摘要失败: %w", index+1, err)
		}
		size, err := layer.Size()
		if err != nil {
			return fmt.Errorf("读取第 %d 层大小失败: %w", index+1, err)
		}
		item := &dockerLayerProgress{
			index:  index + 1,
			digest: digest.String(),
			size:   size,
		}
		p.layers[item.digest] = item
		p.order = append(p.order, item)
		p.total += size
	}
	return nil
}

func (p *dockerProgress) totalSize() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

func (p *dockerProgress) begin(digest string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	layer, ok := p.layers[digest]
	if !ok {
		return false
	}
	layer.started = true
	p.active = layer
	return true
}

func (p *dockerProgress) nextPendingDigest() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, layer := range p.order {
		if !layer.finished {
			return layer.digest
		}
	}
	return ""
}

func (p *dockerProgress) add(digest string, n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	layer, ok := p.layers[digest]
	if !ok {
		return
	}
	layer.downloaded += n
	if layer.downloaded > layer.size {
		layer.downloaded = layer.size
	}
}

func (p *dockerProgress) finish(digest string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	layer, ok := p.layers[digest]
	if !ok {
		return
	}
	layer.finished = true
	layer.downloaded = layer.size
}

type dockerProgressSnapshot struct {
	active      bool
	index       int
	totalLayers int
	digest      string
	done        int64
	total       int64
	totalDone   int64
	totalSize   int64
}

func (p *dockerProgress) snapshot() dockerProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	snapshot := dockerProgressSnapshot{
		totalLayers: len(p.order),
		totalSize:   p.total,
	}
	for _, layer := range p.order {
		snapshot.totalDone += layer.downloaded
	}
	if p.active != nil {
		snapshot.active = true
		snapshot.index = p.active.index
		snapshot.digest = p.active.digest
		snapshot.done = p.active.downloaded
		snapshot.total = p.active.size
	}
	return snapshot
}

func (p *dockerProgress) printSummary() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, layer := range p.order {
		fmt.Printf("[层 %d/%d] %s 完成 (%.1fMB)\n",
			layer.index, len(p.order), shortDigest(layer.digest), float64(layer.size)/(1024*1024))
	}
}

func shortDigest(digest string) string {
	const maxLength = 19
	if len(digest) > maxLength {
		return digest[:maxLength] + "..."
	}
	return digest
}

// startDockerProgress 启动 500ms 刷新的层进度输出。
func startDockerProgress(progress *dockerProgress) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		lastTime := time.Now()
		var lastDone int64

		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				snapshot := progress.snapshot()
				if !snapshot.active {
					continue
				}

				elapsed := now.Sub(lastTime).Seconds()
				speed := 0.0
				if elapsed > 0 {
					speed = float64(snapshot.totalDone-lastDone) / elapsed
				}
				percent := 0.0
				if snapshot.total > 0 {
					percent = float64(snapshot.done) / float64(snapshot.total) * 100
				}
				remaining := snapshot.total - snapshot.done
				eta := "--:--"
				if remaining <= 0 {
					eta = "00:00"
				} else if speed >= 1 {
					eta = core.FormatETA(float64(remaining) / speed)
				}

				core.Logf("\r[层 %d/%d] %s | %5.1f%% | %.1fMB/%.1fMB | 速度: %.1fMB/s | 剩余: %s",
					snapshot.index, snapshot.totalLayers, shortDigest(snapshot.digest), percent,
					float64(snapshot.done)/(1024*1024),
					float64(snapshot.total)/(1024*1024),
					speed/(1024*1024),
					eta)

				lastTime = now
				lastDone = snapshot.totalDone
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			core.Logf("\n")
		})
	}
}

// progressTransport 包装 registry HTTP 响应，统计层下载字节数并应用限速。
type progressTransport struct {
	base     http.RoundTripper
	progress *dockerProgress
}

func (t *progressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.Method != http.MethodGet || resp.Body == nil {
		return resp, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return resp, nil
	}

	digest := layerDigestFromURL(req.URL)
	if !t.progress.begin(digest) {
		if !isLayerContentType(resp.Header.Get("Content-Type")) {
			return resp, nil
		}
		digest = t.progress.nextPendingDigest()
		if !t.progress.begin(digest) {
			return resp, nil
		}
	}

	resp.Body = &trackedLayerReader{
		reader:   core.NewRateLimitedReader(resp.Body),
		closer:   resp.Body,
		progress: t.progress,
		digest:   digest,
	}
	return resp, nil
}

// trackedLayerReader 在读取响应体时累计层进度。
type trackedLayerReader struct {
	reader   io.Reader
	closer   io.Closer
	progress *dockerProgress
	digest   string
}

func (r *trackedLayerReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.progress.add(r.digest, int64(n))
	}
	if errors.Is(err, io.EOF) {
		r.progress.finish(r.digest)
	}
	return n, err
}

func (r *trackedLayerReader) Close() error {
	return r.closer.Close()
}

var layerDigestPattern = regexp.MustCompile(`sha256[:/]([0-9a-f]{64})`)

// layerDigestFromURL 从 registry 或 CDN 的 URL 中提取层摘要。
func layerDigestFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	match := layerDigestPattern.FindStringSubmatch(u.Path)
	if len(match) != 2 {
		return ""
	}
	return "sha256:" + match[1]
}

// isLayerContentType 判断响应是否可能是压缩层数据。
func isLayerContentType(value string) bool {
	value = strings.ToLower(value)
	if strings.Contains(value, "json") || strings.Contains(value, "manifest") {
		return false
	}
	return strings.Contains(value, "layer") ||
		strings.Contains(value, "gzip") ||
		strings.Contains(value, "zstd") ||
		strings.Contains(value, "octet-stream")
}
