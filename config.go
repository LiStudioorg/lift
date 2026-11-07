package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// appConfig 对应 ~/.lift/config.json 的内容。
type appConfig struct {
	DownloadDir string       `json:"download_dir"`
	Limit       int          `json:"limit"`
	Proxy       string       `json:"proxy"`
	Headers     []string     `json:"headers"`
	Mirrors     []string     `json:"mirrors"`
	S3          configS3     `json:"s3"`
	SFTP        configSFTP   `json:"sftp"`
	Listen      configListen `json:"listen"`
}

type configS3 struct {
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token"`
	Region       string `json:"region"`
}

type configSFTP struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	User          string `json:"user"`
	Password      string `json:"password"`
	KeyPath       string `json:"private_key"`
	KeyPassphrase string `json:"private_key_passphrase"`
	KnownHosts    string `json:"known_hosts"`
	Insecure      bool   `json:"insecure"`
}

type configListen struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// configFilePath 返回配置文件路径。LIFT_CONFIG 可用于覆盖默认路径（便于测试）。
func configFilePath() string {
	if path := strings.TrimSpace(os.Getenv("LIFT_CONFIG")); path != "" {
		return expandHome(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lift", "config.json")
}

// loadConfig 读取用户配置文件。默认路径不存在时返回空配置。
func loadConfig() (*appConfig, error) {
	path := configFilePath()
	if path == "" {
		return &appConfig{}, nil
	}
	return loadConfigFrom(path)
}

// loadConfigFrom 从指定路径读取配置，供启动流程和测试使用。
func loadConfigFrom(path string) (*appConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("LIFT_CONFIG") == "" {
			return &appConfig{}, nil
		}
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	var cfg appConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}

	cfg.DownloadDir = expandHome(strings.TrimSpace(cfg.DownloadDir))
	cfg.Proxy = strings.TrimSpace(cfg.Proxy)
	cfg.S3.Endpoint = strings.TrimSpace(cfg.S3.Endpoint)
	cfg.S3.Bucket = strings.TrimSpace(cfg.S3.Bucket)
	cfg.S3.AccessKey = strings.TrimSpace(cfg.S3.AccessKey)
	cfg.S3.SecretKey = strings.TrimSpace(cfg.S3.SecretKey)
	cfg.S3.SessionToken = strings.TrimSpace(cfg.S3.SessionToken)
	cfg.S3.Region = strings.TrimSpace(cfg.S3.Region)
	cfg.SFTP.Host = strings.TrimSpace(cfg.SFTP.Host)
	cfg.SFTP.User = strings.TrimSpace(cfg.SFTP.User)
	cfg.SFTP.KeyPath = expandHome(strings.TrimSpace(cfg.SFTP.KeyPath))
	cfg.SFTP.KnownHosts = expandHome(strings.TrimSpace(cfg.SFTP.KnownHosts))
	cfg.Listen.Host = strings.TrimSpace(cfg.Listen.Host)

	for _, header := range cfg.Headers {
		if _, _, err := splitHeader(header); err != nil {
			return nil, fmt.Errorf("配置文件 %s 的 headers 无效: %w", path, err)
		}
	}
	cfg.Mirrors = normalizeMirrors(cfg.Mirrors)
	return &cfg, nil
}

// normalizeMirrors 清理配置中的镜像模板，并展开每个条目内的逗号分隔写法。
func normalizeMirrors(values []string) []string {
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

// configS3Options 将配置转换为 S3 连接参数。
func (c *appConfig) configS3Options() s3Options {
	return s3Options{
		Endpoint:     c.S3.Endpoint,
		AccessKey:    c.S3.AccessKey,
		SecretKey:    c.S3.SecretKey,
		SessionToken: c.S3.SessionToken,
		Region:       c.S3.Region,
	}
}

// configSFTPOptions 将配置转换为 SFTP 连接参数。
func (c *appConfig) configSFTPOptions() sftpOptions {
	return sftpOptions{
		Host:           c.SFTP.Host,
		Port:           c.SFTP.Port,
		User:           c.SFTP.User,
		Password:       c.SFTP.Password,
		KeyPath:        c.SFTP.KeyPath,
		KeyPassphrase:  c.SFTP.KeyPassphrase,
		KnownHostsPath: c.SFTP.KnownHosts,
		Insecure:       c.SFTP.Insecure,
	}
}

// visitedFlags 记录命令行中显式设置的选项，用于实现 CLI > 配置文件 > 默认值。
func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}

// applyStringDefault 仅在命令行未设置该选项时套用配置值。
func applyStringDefault(target *string, visited map[string]bool, name, value string) {
	if !visited[name] && value != "" {
		*target = value
	}
}

// applyIntDefault 仅在命令行未设置该选项时套用配置值。
func applyIntDefault(target *int, visited map[string]bool, name string, value int) {
	if !visited[name] && value > 0 {
		*target = value
	}
}

// applyBoolDefault 仅在命令行未设置该选项时套用配置值。
func applyBoolDefault(target *bool, visited map[string]bool, name string, value bool) {
	if !visited[name] && value {
		*target = true
	}
}

// resolveConfigOutput 在未指定 -o 时，把相对文件名落到配置的下载目录。
func resolveConfigOutput(cfg *appConfig, output, baseName string) (string, error) {
	if output != "" || cfg == nil || cfg.DownloadDir == "" {
		return output, nil
	}
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		return "", fmt.Errorf("创建配置下载目录失败: %w", err)
	}
	return filepath.Join(cfg.DownloadDir, baseName), nil
}

// headerList 实现 flag.Value，支持多次指定 -H。
type headerList []string

func (h *headerList) String() string {
	return strings.Join(*h, ", ")
}

func (h *headerList) Set(value string) error {
	if _, _, err := splitHeader(value); err != nil {
		return err
	}
	*h = append(*h, value)
	return nil
}

// splitHeader 解析 "Name: value" 形式的请求头。
func splitHeader(line string) (string, string, error) {
	name, value, ok := strings.Cut(line, ":")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return "", "", fmt.Errorf("请求头格式应为 \"Name: value\": %q", line)
	}
	if !validHeaderName(name) {
		return "", "", fmt.Errorf("请求头名称无效: %q", name)
	}
	return name, strings.TrimSpace(value), nil
}

// validHeaderName 检查 RFC 7230 token 形式的请求头名称。
func validHeaderName(name string) bool {
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

// headersFromLines 将多行请求头转换为 http.Header。
func headersFromLines(lines []string) (http.Header, error) {
	headers := make(http.Header)
	for _, line := range lines {
		name, value, err := splitHeader(line)
		if err != nil {
			return nil, err
		}
		headers.Add(name, value)
	}
	return headers, nil
}

// parseMirrorFlag 解析 -mirror 的逗号分隔模板列表。
func parseMirrorFlag(value string) []string {
	var mirrors []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			mirrors = append(mirrors, part)
		}
	}
	return mirrors
}
