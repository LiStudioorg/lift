// Package version 保存构建时注入的版本信息。
package version

import "fmt"

var (
	// Version 是程序版本号，发布构建时由 ldflags 覆盖。
	Version = "v0.1.0"
	// GitCommit 是构建对应的 Git 提交。
	GitCommit = "unknown"
	// BuiltAt 是构建时间。
	BuiltAt = "unknown"
)

// String 返回完整版本信息。
func String() string {
	return fmt.Sprintf("lift %s (commit %s, built %s)", Version, GitCommit, BuiltAt)
}
