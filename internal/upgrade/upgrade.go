// Package upgrade 实现 lift 的自更新命令。
package upgrade

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/LiStudioorg/lift/internal/version"
)

const (
	repository = "LiStudioorg/lift"
	apiURL     = "https://api.github.com/repos/" + repository + "/releases/latest"
)

// release 是 GitHub Release 的必要字段。
type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

// asset 是 Release 中的一个下载资源。
type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Options 是 upgrade 子命令的参数。
type Options struct {
	Force bool
}

// CLI 实现 "lift upgrade" 子命令。
func CLI(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = usage
	force := fs.Bool("force", false, "强制重新安装最新版本")
	forceShort := fs.Bool("f", false, "强制重新安装最新版本")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "参数错误: upgrade 不接受位置参数: %s\n", strings.Join(fs.Args(), " "))
		usage()
		return 2
	}

	if err := Run(context.Background(), Options{Force: *force || *forceShort}); err != nil {
		fmt.Fprintf(os.Stderr, "升级失败: %v\n", err)
		return 1
	}
	return 0
}

// usage 输出 upgrade 子命令帮助。
func usage() {
	fmt.Fprintln(os.Stderr, "用法: lift upgrade [-f|--force]")
	fmt.Fprintln(os.Stderr, "检查 GitHub 最新发布版；有新版本时下载并覆盖当前可执行文件。")
	fmt.Fprintln(os.Stderr, "  -f, --force   当前已是最新版本时仍重新下载并安装")
}

// Run 检查并安装最新版本。
func Run(ctx context.Context, options Options) error {
	if runtime.GOOS == "js" || runtime.GOOS == "wasip1" {
		return errors.New("当前平台不支持自更新，请使用安装脚本")
	}

	apiClient := &http.Client{Timeout: 30 * time.Second}
	releaseInfo, err := fetchLatestRelease(ctx, apiClient)
	if err != nil {
		return err
	}
	if releaseInfo.TagName == "" {
		return errors.New("最新版本信息缺少 tag_name")
	}
	if releaseInfo.TagName == version.Version && !options.Force {
		fmt.Printf("当前已是最新版本 %s，无需升级。\n", version.Version)
		return nil
	}

	fmt.Printf("当前版本: %s\n", version.Version)
	fmt.Printf("最新版本: %s\n", releaseInfo.TagName)

	selected, err := findAsset(releaseInfo, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	executable, err := executablePath()
	if err != nil {
		return err
	}
	cleanupOldExecutable(executable)

	tempDir, err := os.MkdirTemp("", "lift-upgrade-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, selected.Name)
	downloadClient := &http.Client{Timeout: 15 * time.Minute}
	fmt.Printf("正在下载 %s ...\n", selected.Name)
	if err := download(ctx, downloadClient, selected.BrowserDownloadURL, archivePath); err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}

	binaryName := "lift"
	if runtime.GOOS == "windows" {
		binaryName = "lift.exe"
	}
	extractedPath := filepath.Join(tempDir, binaryName)
	fmt.Println("正在解压 ...")
	if err := extractBinary(archivePath, binaryName, extractedPath); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}

	fmt.Printf("正在替换 %s ...\n", executable)
	if err := replaceExecutable(executable, extractedPath); err != nil {
		return fmt.Errorf("替换可执行文件失败: %w", err)
	}

	fmt.Printf("已升级到 %s。\n", releaseInfo.TagName)
	return nil
}

// fetchLatestRelease 获取 GitHub 最新发布版信息。
func fetchLatestRelease(ctx context.Context, client *http.Client) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 GitHub API 请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lift-upgrade/"+version.Version)
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GitHub API 返回 %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var releaseInfo release
	if err := json.NewDecoder(resp.Body).Decode(&releaseInfo); err != nil {
		return nil, fmt.Errorf("解析 GitHub API 响应失败: %w", err)
	}
	return &releaseInfo, nil
}

// assetName 返回指定平台发布包的名称。
func assetName(tag, goos, goarch string) string {
	versionPart := strings.TrimPrefix(tag, "v")
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return fmt.Sprintf("lift_%s_%s_%s%s", versionPart, goos, goarch, extension)
}

// findAsset 在 Release 中查找当前平台的发布包。
func findAsset(releaseInfo *release, goos, goarch string) (asset, error) {
	expected := assetName(releaseInfo.TagName, goos, goarch)
	for _, item := range releaseInfo.Assets {
		if item.Name != expected {
			continue
		}
		if item.BrowserDownloadURL == "" {
			return asset{}, fmt.Errorf("发布包 %s 缺少下载地址", expected)
		}
		return item, nil
	}
	return asset{}, fmt.Errorf("最新版本 %s 不提供 %s/%s 的安装包（期望 %s）", releaseInfo.TagName, goos, goarch, expected)
}

// executablePath 返回当前可执行文件的真实路径。
func executablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取当前可执行文件路径失败: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析可执行文件绝对路径失败: %w", err)
	}
	return absPath, nil
}

// download 下载文件，失败时最多重试三次。
func download(ctx context.Context, client *http.Client, url, destination string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt-1) * time.Second):
			}
		}
		lastErr = downloadOnce(ctx, client, url, destination)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// downloadOnce 执行一次下载。
func downloadOnce(ctx context.Context, client *http.Client, url, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("创建下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "lift-upgrade/"+version.Version)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		os.Remove(destination)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(destination)
		return closeErr
	}
	return nil
}

// extractBinary 从 tar.gz 或 zip 归档中提取单个可执行文件。
func extractBinary(archivePath, binaryName, destination string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"), strings.HasSuffix(archivePath, ".tgz"):
		return extractTarGz(archivePath, binaryName, destination)
	case strings.HasSuffix(archivePath, ".zip"):
		return extractZip(archivePath, binaryName, destination)
	default:
		return fmt.Errorf("不支持的归档格式: %s", filepath.Base(archivePath))
	}
}

// extractTarGz 从 tar.gz 归档中提取可执行文件。
func extractTarGz(archivePath, binaryName, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}
		if filepath.Base(header.Name) != binaryName {
			continue
		}
		return writeBinary(destination, tarReader, header.FileInfo().Mode())
	}
	return fmt.Errorf("归档中未找到 %s", binaryName)
}

// extractZip 从 zip 归档中提取可执行文件。
func extractZip(archivePath, binaryName, destination string) error {
	zipReader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zipReader.Close()

	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() || filepath.Base(file.Name) != binaryName {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return err
		}
		writeErr := writeBinary(destination, reader, file.Mode())
		closeErr := reader.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		return nil
	}
	return fmt.Errorf("归档中未找到 %s", binaryName)
}

// writeBinary 将可执行文件写入目标路径。
func writeBinary(destination string, reader io.Reader, mode os.FileMode) error {
	permission := mode.Perm()
	if permission&0o111 == 0 {
		permission = 0o755
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, permission)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, reader); err != nil {
		file.Close()
		os.Remove(destination)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(destination)
		return err
	}
	return os.Chmod(destination, permission)
}

// replaceExecutable 用新文件替换当前可执行文件。
func replaceExecutable(target, source string) error {
	if runtime.GOOS == "windows" {
		return replaceWindowsExecutable(target, source)
	}
	return replaceUnixExecutable(target, source)
}

// replaceUnixExecutable 通过同目录临时文件原子替换可执行文件。
func replaceUnixExecutable(target, source string) error {
	dir := filepath.Dir(target)
	tempFile, err := os.CreateTemp(dir, ".lift-upgrade-*")
	if err != nil {
		return fmt.Errorf("目标目录不可写，请使用 sudo 或重新运行安装脚本: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := copyFile(source, tempPath, 0o755); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

// replaceWindowsExecutable 在 Windows 上先将旧文件重命名为 .old，再写入新文件。
func replaceWindowsExecutable(target, source string) error {
	oldPath := target + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(target, oldPath); err != nil {
		oldPath = fmt.Sprintf("%s.old.%d", target, time.Now().UnixNano())
		if renameErr := os.Rename(target, oldPath); renameErr != nil {
			return fmt.Errorf("无法重命名正在运行的二进制，请关闭 lift 后重试: %w", renameErr)
		}
	}
	if err := copyFile(source, target, 0o755); err != nil {
		_ = os.Remove(target)
		_ = os.Rename(oldPath, target)
		return err
	}
	if err := os.Remove(oldPath); err != nil {
		fmt.Printf("旧版本文件将在下次升级时清理: %s\n", oldPath)
	}
	return nil
}

// cleanupOldExecutable 清理上次升级遗留的旧文件。
func cleanupOldExecutable(target string) {
	oldPath := target + ".old"
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		// 文件仍被占用时保留，留待下次升级处理。
	}
	matches, err := filepath.Glob(target + ".old.*")
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

// copyFile 复制文件并设置权限。
func copyFile(source, destination string, permission os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, permission)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		os.Remove(destination)
		return err
	}
	if err := output.Close(); err != nil {
		os.Remove(destination)
		return err
	}
	return os.Chmod(destination, permission)
}
