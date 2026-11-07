package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// NormalizeSHA256 校验并规范化用户提供的 SHA256 值。
func NormalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != sha256.Size*2 {
		return "", fmt.Errorf("SHA256 必须是 %d 位十六进制字符串", sha256.Size*2)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("SHA256 格式无效: %w", err)
	}
	return value, nil
}

// VerifySHA256 重新打开文件，计算 SHA256 并与期望值比较。
func VerifySHA256(path, expected string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件进行 SHA256 校验失败: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", fmt.Errorf("读取文件进行 SHA256 校验失败: %w", err)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return actual, fmt.Errorf("SHA256 校验失败: 期望 %s，实际 %s", expected, actual)
	}
	return actual, nil
}
