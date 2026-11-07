package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/LiStudioorg/lift/internal/core"
)

// RunSource 下载 src，校验可选的 SHA256，成功后删除断点续传状态，并返回进程退出码。
func RunSource(ctx context.Context, src core.Source, outputPath, expectedHash string) int {
	defer core.CloseSource(src)

	if err := core.Download(ctx, src, outputPath); err != nil {
		return HandleDownloadError(err)
	}

	if expectedHash != "" {
		actual, err := core.VerifySHA256(outputPath, expectedHash)
		if err != nil {
			return HandleDownloadError(err)
		}
		fmt.Printf("SHA256 校验通过 (%s): %s\n", outputPath, actual)
	}

	if err := os.Remove(core.StatePath(outputPath)); err != nil && !os.IsNotExist(err) {
		return HandleDownloadError(fmt.Errorf("删除状态文件失败: %w", err))
	}
	return 0
}

// HandleDownloadError 输出下载错误并返回约定退出码（取消为 130，其他为 1）。
func HandleDownloadError(err error) int {
	if errors.Is(err, context.Canceled) {
		fmt.Println("下载已取消，可使用相同命令继续下载。")
		return 130
	}
	fmt.Fprintf(os.Stderr, "下载失败: %v\n", err)
	return 1
}
