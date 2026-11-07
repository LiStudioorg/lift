package core

import (
	"encoding/json"
	"fmt"
	"os"
)

// Chunk 是文件中可断点续传的区间。
type Chunk struct {
	Start      int64 `json:"start"`
	End        int64 `json:"end"`
	Downloaded int64 `json:"downloaded"`
}

// DownloadState 保存断点续传所需的元数据。
type DownloadState struct {
	URL    string  `json:"url"`
	ETag   string  `json:"etag"`
	Size   int64   `json:"size"`
	Chunks []Chunk `json:"chunks"`
}

// StatePath 返回输出文件对应的状态文件路径。
func StatePath(outputPath string) string {
	return outputPath + ".lift"
}

// SaveState 通过写入-同步-重命名的方式原子保存状态。
func SaveState(path string, state *DownloadState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化下载状态失败: %w", err)
	}

	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("创建临时状态文件失败: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("写入临时状态文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("同步临时状态文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("关闭临时状态文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("替换状态文件失败: %w", err)
	}
	return nil
}

// LoadState 读取并解析状态文件。
func LoadState(path string) (*DownloadState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state DownloadState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析状态文件失败: %w", err)
	}
	return &state, nil
}
