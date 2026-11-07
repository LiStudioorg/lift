// Package config 负责加载 lift 的 TOML 配置，并提供命令行优先级辅助函数。
package config

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config 是配置文件的根结构。
type Config struct {
	DownloadDir string   `toml:"download_dir"`
	Limit       int      `toml:"limit"`
	Proxy       string   `toml:"proxy"`
	Headers     []string `toml:"headers"`
	Mirrors     []string `toml:"mirrors"`
	S3          S3       `toml:"s3"`
	SFTP        SFTP     `toml:"sftp"`
	Listen      Listen   `toml:"listen"`
}

// S3 保存兼容 S3 的对象存储默认参数。
type S3 struct {
	Endpoint     string `toml:"endpoint"`
	Bucket       string `toml:"bucket"`
	AccessKey    string `toml:"access_key"`
	SecretKey    string `toml:"secret_key"`
	SessionToken string `toml:"session_token"`
	Region       string `toml:"region"`
}

// SFTP 保存 SFTP 默认参数。
type SFTP struct {
	Host          string `toml:"host"`
	Port          int    `toml:"port"`
	User          string `toml:"user"`
	Password      string `toml:"password"`
	KeyPath       string `toml:"private_key"`
	KeyPassphrase string `toml:"private_key_passphrase"`
	KnownHosts    string `toml:"known_hosts"`
	Insecure      bool   `toml:"insecure"`
}

// Listen 保存 HTTP API 服务默认参数。
type Listen struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

// DefaultPath 返回当前生效的配置路径。LIFT_CONFIG 可覆盖默认的
// ~/.lift/config.toml 路径，便于测试和服务部署。
func DefaultPath() string {
	if path := strings.TrimSpace(os.Getenv("LIFT_CONFIG")); path != "" {
		return ExpandHome(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lift", "config.toml")
}

// Load 读取当前生效的配置。默认配置文件不存在时不视为错误。
func Load() (*Config, error) {
	path := DefaultPath()
	if path == "" {
		return &Config{}, nil
	}
	return LoadFrom(path)
}

// LoadFrom 从指定路径读取 TOML 配置。
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("LIFT_CONFIG") == "" {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	cfg := Config{}
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}
	for _, key := range meta.Undecoded() {
		fmt.Fprintf(os.Stderr, "[配置] 忽略未知字段: %s\n", key)
	}

	normalize(&cfg)
	for _, header := range cfg.Headers {
		if _, _, err := SplitHeader(header); err != nil {
			return nil, fmt.Errorf("配置文件 %s 的 headers 无效: %w", path, err)
		}
	}
	cfg.Mirrors = NormalizeMirrors(cfg.Mirrors)
	return &cfg, nil
}

func normalize(cfg *Config) {
	cfg.DownloadDir = ExpandHome(strings.TrimSpace(cfg.DownloadDir))
	cfg.Proxy = strings.TrimSpace(cfg.Proxy)
	cfg.S3.Endpoint = strings.TrimSpace(cfg.S3.Endpoint)
	cfg.S3.Bucket = strings.TrimSpace(cfg.S3.Bucket)
	cfg.S3.AccessKey = strings.TrimSpace(cfg.S3.AccessKey)
	cfg.S3.SecretKey = strings.TrimSpace(cfg.S3.SecretKey)
	cfg.S3.SessionToken = strings.TrimSpace(cfg.S3.SessionToken)
	cfg.S3.Region = strings.TrimSpace(cfg.S3.Region)
	cfg.SFTP.Host = strings.TrimSpace(cfg.SFTP.Host)
	cfg.SFTP.User = strings.TrimSpace(cfg.SFTP.User)
	cfg.SFTP.KeyPath = ExpandHome(strings.TrimSpace(cfg.SFTP.KeyPath))
	cfg.SFTP.KnownHosts = ExpandHome(strings.TrimSpace(cfg.SFTP.KnownHosts))
	cfg.Listen.Host = strings.TrimSpace(cfg.Listen.Host)
}

// ExpandHome 将开头的 ~/（或单独的 ~）展开为用户主目录。
func ExpandHome(name string) string {
	if name == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return name
	}
	if !strings.HasPrefix(name, "~/") {
		return name
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return name
	}
	return filepath.Join(home, name[2:])
}

// NormalizeMirrors 去除镜像模板首尾空白，并展开逗号分隔的条目。
func NormalizeMirrors(values []string) []string {
	var mirrors []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !strings.HasPrefix(part, "http://") && !strings.HasPrefix(part, "https://") {
				fmt.Fprintf(os.Stderr, "[配置] 忽略无效镜像模板（仅支持 http/https）: %s\n", part)
				continue
			}
			mirrors = append(mirrors, part)
		}
	}
	return mirrors
}

// VisitedFlags 记录命令行中显式设置的选项。
func VisitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}

// ApplyStringDefault 仅在命令行未设置选项时应用配置值。
func ApplyStringDefault(target *string, visited map[string]bool, name, value string) {
	if !visited[name] && value != "" {
		*target = value
	}
}

// ApplyIntDefault 仅在命令行未设置选项时应用正的配置值。
func ApplyIntDefault(target *int, visited map[string]bool, name string, value int) {
	if !visited[name] && value > 0 {
		*target = value
	}
}

// ApplyBoolDefault 仅在命令行未设置选项时应用为 true 的配置值。
func ApplyBoolDefault(target *bool, visited map[string]bool, name string, value bool) {
	if !visited[name] && value {
		*target = true
	}
}

// ResolveOutput 在命令行未指定输出时，将相对输出名放入配置的下载目录。
func ResolveOutput(cfg *Config, output, baseName string) (string, error) {
	if output != "" || cfg == nil || cfg.DownloadDir == "" {
		return output, nil
	}
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		return "", fmt.Errorf("创建配置下载目录失败: %w", err)
	}
	return filepath.Join(cfg.DownloadDir, baseName), nil
}

// HeaderList 实现 flag.Value，支持重复指定 -H 参数。
type HeaderList []string

func (h *HeaderList) String() string {
	return strings.Join(*h, ", ")
}

func (h *HeaderList) Set(value string) error {
	if _, _, err := SplitHeader(value); err != nil {
		return err
	}
	*h = append(*h, value)
	return nil
}

// SplitHeader 解析 "Name: value" 格式的请求头。
func SplitHeader(line string) (string, string, error) {
	name, value, ok := strings.Cut(line, ":")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return "", "", fmt.Errorf("请求头格式应为 \"Name: value\": %q", line)
	}
	if !ValidHeaderName(name) {
		return "", "", fmt.Errorf("请求头名称无效: %q", name)
	}
	return name, strings.TrimSpace(value), nil
}

// ValidHeaderName 检查请求头名称是否符合 RFC 7230 token 规则。
func ValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// HeadersFromLines 将请求头文本行转换为 http.Header。
func HeadersFromLines(lines []string) (http.Header, error) {
	headers := make(http.Header)
	for _, line := range lines {
		name, value, err := SplitHeader(line)
		if err != nil {
			return nil, err
		}
		headers.Add(name, value)
	}
	return headers, nil
}

// ParseMirrorFlag 解析逗号分隔的 -mirror 参数。
func ParseMirrorFlag(value string) []string {
	var mirrors []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			mirrors = append(mirrors, part)
		}
	}
	return mirrors
}
