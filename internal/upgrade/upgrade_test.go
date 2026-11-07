package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestAssetName(t *testing.T) {
	tests := []struct {
		name     string
		tag      string
		goos     string
		goarch   string
		expected string
	}{
		{
			name:     "Linux amd64",
			tag:      "v0.1.0",
			goos:     "linux",
			goarch:   "amd64",
			expected: "lift_0.1.0_linux_amd64.tar.gz",
		},
		{
			name:     "Windows arm64",
			tag:      "v0.1.0",
			goos:     "windows",
			goarch:   "arm64",
			expected: "lift_0.1.0_windows_arm64.zip",
		},
		{
			name:     "无 v 前缀",
			tag:      "0.2.0",
			goos:     "darwin",
			goarch:   "arm64",
			expected: "lift_0.2.0_darwin_arm64.tar.gz",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := assetName(test.tag, test.goos, test.goarch)
			if actual != test.expected {
				t.Fatalf("assetName() = %q, want %q", actual, test.expected)
			}
		})
	}
}

func TestFindAsset(t *testing.T) {
	releaseInfo := &release{
		TagName: "v0.1.0",
		Assets: []asset{
			{
				Name:               "lift_0.1.0_linux_amd64.tar.gz",
				BrowserDownloadURL: "https://example.com/linux-amd64.tar.gz",
			},
			{
				Name:               "lift_0.1.0_windows_arm64.zip",
				BrowserDownloadURL: "https://example.com/windows-arm64.zip",
			},
		},
	}

	selected, err := findAsset(releaseInfo, "linux", "amd64")
	if err != nil {
		t.Fatalf("findAsset() error = %v", err)
	}
	if selected.BrowserDownloadURL != "https://example.com/linux-amd64.tar.gz" {
		t.Fatalf("findAsset() URL = %q", selected.BrowserDownloadURL)
	}

	if _, err := findAsset(releaseInfo, "linux", "s390x"); err == nil {
		t.Fatal("findAsset() 对不存在的平台应返回错误")
	}
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "lift.tar.gz")
	content := []byte("tar binary")
	writeTarGz(t, archivePath, "lift", content)

	destination := filepath.Join(dir, "extracted-lift")
	if err := extractBinary(archivePath, "lift", destination); err != nil {
		t.Fatalf("extractBinary() error = %v", err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("读取解压文件失败: %v", err)
	}
	if !bytes.Equal(actual, content) {
		t.Fatalf("解压内容 = %q, want %q", actual, content)
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "lift.zip")
	content := []byte("zip binary")
	writeZip(t, archivePath, "lift.exe", content)

	destination := filepath.Join(dir, "extracted-lift.exe")
	if err := extractBinary(archivePath, "lift.exe", destination); err != nil {
		t.Fatalf("extractBinary() error = %v", err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("读取解压文件失败: %v", err)
	}
	if !bytes.Equal(actual, content) {
		t.Fatalf("解压内容 = %q, want %q", actual, content)
	}
}

func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "lift")
	source := filepath.Join(dir, "lift-new")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatalf("写入旧文件失败: %v", err)
	}
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatalf("写入新文件失败: %v", err)
	}

	if err := replaceExecutable(target, source); err != nil {
		t.Fatalf("replaceExecutable() error = %v", err)
	}
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取替换后的文件失败: %v", err)
	}
	if string(actual) != "new" {
		t.Fatalf("替换后的内容 = %q, want %q", actual, "new")
	}
}

func writeTarGz(t *testing.T, path, name string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 tar.gz 失败: %v", err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("写入 tar 头失败: %v", err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatalf("写入 tar 内容失败: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭 tar.gz 文件失败: %v", err)
	}
}

func writeZip(t *testing.T, path, name string, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zip 失败: %v", err)
	}
	zipWriter := zip.NewWriter(file)
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o755)
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		t.Fatalf("创建 zip 条目失败: %v", err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("写入 zip 内容失败: %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭 zip 文件失败: %v", err)
	}
}
