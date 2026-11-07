// Package cli 提供根命令和协议子命令共享的命令行解析与下载完成辅助逻辑。
package cli

import (
	"flag"
	"fmt"
	"strings"
)

// ReorderArgs 将选项移到位置参数之前，使选项可以出现在 URL 前后。未知选项留给 flag.Parse 报告。
func ReorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags []string
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if name == "" || strings.Contains(name, "=") {
			continue
		}

		defined := fs.Lookup(name)
		if defined == nil {
			continue
		}
		if boolFlag, ok := defined.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positional...)
}

// ParsePullArgs 解析 "pull <positional> [options]" 形式的参数，允许选项出现在位置值前后。
func ParsePullArgs(fs *flag.FlagSet, args []string, positionalName string) (string, error) {
	var positional string
	var flagArgs []string

	appendPositional := func(value string) error {
		if positional != "" {
			return fmt.Errorf("只能指定一个%s", positionalName)
		}
		positional = value
		return nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			for _, value := range args[i+1:] {
				if err := appendPositional(value); err != nil {
					return "", err
				}
			}
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if err := appendPositional(arg); err != nil {
				return "", err
			}
			continue
		}

		flagArgs = append(flagArgs, arg)
		name := strings.TrimLeft(arg, "-")
		if name == "" || strings.Contains(name, "=") {
			continue
		}

		defined := fs.Lookup(name)
		if defined == nil {
			continue
		}
		if boolFlag, ok := defined.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}

	if err := fs.Parse(flagArgs); err != nil {
		return "", err
	}
	if positional == "" {
		return "", fmt.Errorf("缺少%s", positionalName)
	}
	return positional, nil
}
