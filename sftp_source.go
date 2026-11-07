package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// sftpOptions 是连接 SFTP 服务器所需的参数。
type sftpOptions struct {
	Host           string
	Port           int
	User           string
	Password       string
	KeyPath        string
	KeyPassphrase  string
	KnownHostsPath string
	Insecure       bool
}

// SFTPSource 通过 SFTP 协议读取远端文件。
type SFTPSource struct {
	options    sftpOptions
	remotePath string
	sourceURL  string

	mu     sync.Mutex
	client *sftp.Client
	conn   *ssh.Client
}

// newSFTPSource 创建 SFTP 数据源。
func newSFTPSource(options sftpOptions, remotePath string) (*SFTPSource, error) {
	if strings.TrimSpace(options.Host) == "" {
		return nil, fmt.Errorf("SFTP 主机不能为空")
	}
	if options.User == "" {
		return nil, fmt.Errorf("SFTP 用户名不能为空")
	}
	if remotePath == "" {
		return nil, fmt.Errorf("SFTP 远端路径不能为空")
	}
	if options.Password == "" && options.KeyPath == "" {
		return nil, fmt.Errorf("SFTP 需要提供密码或私钥")
	}

	return &SFTPSource{
		options:    options,
		remotePath: remotePath,
		sourceURL:  sanitizedSFTPURL(options, remotePath),
	}, nil
}

// newSFTPSourceFromURL 从 sftp://user:pass@host:port/path 创建数据源。
func newSFTPSourceFromURL(u *url.URL, options sftpOptions) (*SFTPSource, error) {
	if u.Host == "" {
		return nil, fmt.Errorf("SFTP URL 缺少主机名: %s", u.String())
	}
	options.Host = u.Host
	if u.User != nil {
		if user := u.User.Username(); user != "" {
			options.User = user
		}
		if password, ok := u.User.Password(); ok {
			options.Password = password
		}
	}
	return newSFTPSource(options, u.Path)
}

// sanitizedSFTPURL 生成不包含密码的展示地址，避免密码写入状态文件。
func sanitizedSFTPURL(options sftpOptions, remotePath string) string {
	u := url.URL{
		Scheme: "sftp",
		Host:   options.Host,
		Path:   remotePath,
	}
	if options.User != "" {
		u.User = url.User(options.User)
	}
	return u.String()
}

// URL 返回下载源地址。
func (s *SFTPSource) URL() string {
	return s.sourceURL
}

// Stat 使用 SFTP Stat 获取文件大小，并用 ModTime 作为变更标识。
func (s *SFTPSource) Stat(ctx context.Context) (*ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	client, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	info, err := client.Stat(s.remotePath)
	if err != nil {
		return nil, fmt.Errorf("SFTP Stat %s 失败: %w", s.remotePath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("SFTP 远端路径是目录: %s", s.remotePath)
	}

	return &ObjectInfo{
		Size: info.Size(),
		ETag: info.ModTime().UTC().Format(time.RFC3339Nano),
	}, nil
}

// OpenStream 打开远端文件并定位到指定偏移量。
func (s *SFTPSource) OpenStream(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("SFTP Open %s: 无效偏移量 %d", s.remotePath, offset)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	client, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}

	file, err := client.Open(s.remotePath)
	if err != nil {
		// 连接可能因网络中断失效，丢弃旧连接后重试一次。
		s.invalidate(client)
		client, reconnectErr := s.connect(ctx)
		if reconnectErr != nil {
			return nil, fmt.Errorf("SFTP Open %s 失败: %w", s.remotePath, err)
		}
		file, err = client.Open(s.remotePath)
		if err != nil {
			return nil, fmt.Errorf("SFTP Open %s 失败: %w", s.remotePath, err)
		}
	}

	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return nil, fmt.Errorf("SFTP Seek %s 到偏移 %d 失败: %w", s.remotePath, offset, err)
		}
	}

	return newContextReadCloser(ctx, file), nil
}

// Close 关闭 SFTP 会话和底层 SSH 连接。
func (s *SFTPSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var closeErr error
	if s.client != nil {
		closeErr = s.client.Close()
		s.client = nil
	}
	if s.conn != nil {
		if err := s.conn.Close(); closeErr == nil {
			closeErr = err
		}
		s.conn = nil
	}
	return closeErr
}

// connect 延迟建立 SSH/SFTP 连接，多个下载协程共享同一个客户端。
func (s *SFTPSource) connect(ctx context.Context) (*sftp.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		return s.client, nil
	}

	address, err := s.address()
	if err != nil {
		return nil, err
	}
	authMethods, err := s.authMethods()
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := s.hostKeyCallback()
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User:            s.options.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         20 * time.Second,
	}

	dialer := net.Dialer{Timeout: 20 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("连接 SFTP 服务器 %s 失败: %w", address, err)
	}

	// 为 SSH 握手和 SFTP 子系统协商设置总超时。
	if err := rawConn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("设置 SFTP 连接超时失败: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, address, config)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("SSH 握手失败: %w", err)
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("启动 SFTP 子系统失败: %w", err)
	}
	if err := rawConn.SetDeadline(time.Time{}); err != nil {
		sftpClient.Close()
		sshClient.Close()
		return nil, fmt.Errorf("清除 SFTP 连接超时失败: %w", err)
	}

	s.client = sftpClient
	s.conn = sshClient
	return sftpClient, nil
}

// invalidate 在连接失效时关闭并清除当前客户端。
func (s *SFTPSource) invalidate(stale *sftp.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != stale {
		return
	}
	s.client = nil
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}

// address 将主机和端口组合成 net.Dial 可用的地址。
func (s *SFTPSource) address() (string, error) {
	host := strings.TrimSpace(s.options.Host)
	if host == "" {
		return "", fmt.Errorf("SFTP 主机不能为空")
	}

	if h, p, err := net.SplitHostPort(host); err == nil {
		if p == "" {
			return net.JoinHostPort(h, strconv.Itoa(s.port())), nil
		}
		return net.JoinHostPort(h, p), nil
	}

	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return net.JoinHostPort(host, strconv.Itoa(s.port())), nil
}

func (s *SFTPSource) port() int {
	if s.options.Port > 0 {
		return s.options.Port
	}
	return 22
}

// authMethods 根据密码或私钥构建 SSH 认证方式。
func (s *SFTPSource) authMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if s.options.Password != "" {
		methods = append(methods, ssh.Password(s.options.Password))
	}
	if s.options.KeyPath != "" {
		keyPath := expandHome(s.options.KeyPath)
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("读取 SFTP 私钥失败: %w", err)
		}

		var signer ssh.Signer
		if s.options.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(keyData, []byte(s.options.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(keyData)
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SFTP 私钥失败: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("SFTP 需要提供密码或私钥")
	}
	return methods, nil
}

// hostKeyCallback 优先使用 known_hosts 校验服务器指纹。
func (s *SFTPSource) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if s.options.Insecure {
		return ssh.InsecureIgnoreHostKey(), nil
	}

	knownHostsPath := expandHome(s.options.KnownHostsPath)
	if knownHostsPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := filepath.Join(home, ".ssh", "known_hosts")
			if _, err := os.Stat(candidate); err == nil {
				knownHostsPath = candidate
			}
		}
	}

	if knownHostsPath != "" {
		callback, err := knownhosts.New(knownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("加载 SFTP known_hosts 失败: %w", err)
		}
		return callback, nil
	}

	fmt.Fprintln(os.Stderr, "警告: 未找到 SFTP known_hosts，跳过主机密钥校验；生产环境请使用 --sftp-known-hosts 或 --sftp-insecure 明确配置")
	return ssh.InsecureIgnoreHostKey(), nil
}

// expandHome 展开路径开头的 ~/。
func expandHome(name string) string {
	if !strings.HasPrefix(name, "~/") {
		return name
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return name
	}
	return filepath.Join(home, name[2:])
}

// contextReadCloser 在上下文取消时主动关闭底层文件，避免阻塞读取。
type contextReadCloser struct {
	io.ReadCloser
	done chan struct{}
	once sync.Once
}

func newContextReadCloser(ctx context.Context, rc io.ReadCloser) io.ReadCloser {
	reader := &contextReadCloser{
		ReadCloser: rc,
		done:       make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			reader.Close()
		case <-reader.done:
		}
	}()
	return reader
}

func (r *contextReadCloser) Close() error {
	var err error
	r.once.Do(func() {
		close(r.done)
		err = r.ReadCloser.Close()
	})
	return err
}

// sftpCLI 处理 sftp pull 子命令。
func sftpCLI(args []string) {
	if len(args) == 0 || args[0] != "pull" {
		sftpUsage()
		os.Exit(2)
	}

	fs := flag.NewFlagSet("sftp pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	host := fs.String("host", "", "SFTP 主机（可含端口）")
	port := fs.Int("port", 0, "SFTP 端口（主机未包含端口时使用，默认 22）")
	user := fs.String("user", "", "SFTP 用户名")
	password := fs.String("pass", "", "SFTP 密码")
	keyPath := fs.String("key", "", "SFTP 私钥路径")
	keyPassphrase := fs.String("key-pass", "", "SFTP 私钥密码")
	knownHosts := fs.String("known-hosts", "", "known_hosts 路径")
	insecure := fs.Bool("insecure", false, "跳过主机密钥校验（不推荐）")
	output := fs.String("o", "", "输出文件名")
	limit := fs.Int("limit", 0, "限速（KB/s，0 表示不限速）")

	remotePath, err := parsePullArgs(fs, args[1:], "远端路径")
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		sftpUsage()
		os.Exit(2)
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "参数错误: 缺少 --host")
		sftpUsage()
		os.Exit(2)
	}
	if *output == "" {
		*output = defaultSourceOutputName(remotePath)
	}

	InitLimiter(*limit)

	src, err := newSFTPSource(sftpOptions{
		Host:           *host,
		Port:           *port,
		User:           *user,
		Password:       *password,
		KeyPath:        *keyPath,
		KeyPassphrase:  *keyPassphrase,
		KnownHostsPath: *knownHosts,
		Insecure:       *insecure,
	}, remotePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		os.Exit(2)
	}

	runSourceCLI(src, *output)
}

func sftpUsage() {
	fmt.Fprintln(os.Stderr, "用法: lift sftp pull <remote_path> --host <host[:port]> --user <user> (--pass <password> | --key <private_key>) [-o output] [-limit KB/s]")
}
