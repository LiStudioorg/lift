package core

import (
	"fmt"
	"os"
	"sync"
)

// outputMu 串行化并发下载、协议实现和 HTTP API worker 的控制台输出。
var outputMu sync.Mutex

// Logf 在共享输出锁的保护下向 stdout 写入格式化内容。
func Logf(format string, args ...any) {
	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Printf(format, args...)
}

// LogErrorf 在共享输出锁的保护下向 stderr 写入格式化内容。
func LogErrorf(format string, args ...any) {
	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Fprintf(os.Stderr, format, args...)
}
