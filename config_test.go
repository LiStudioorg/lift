package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFrom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
		"download_dir": "/tmp/lift-downloads",
		"limit": 2048,
		"proxy": "socks5://127.0.0.1:1080",
		"headers": ["Cookie: a=b", "Referer: https://example.com/"],
		"mirrors": ["https://ghproxy.com/{url}", "https://mirror.example/{url_encoded}"],
		"s3": {
			"endpoint": "https://s3.example.com",
			"bucket": "demo",
			"access_key": "ak",
			"secret_key": "sk",
			"session_token": "token",
			"region": "us-east-1"
		},
		"sftp": {
			"host": "sftp.example.com",
			"port": 2222,
			"user": "demo",
			"password": "pass",
			"private_key": "/tmp/key",
			"known_hosts": "/tmp/known_hosts",
			"insecure": true
		},
		"listen": {"host": "127.0.0.1", "port": 16900}
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfigFrom(path)
	if err != nil {
		t.Fatalf("loadConfigFrom failed: %v", err)
	}
	if cfg.DownloadDir != "/tmp/lift-downloads" {
		t.Errorf("DownloadDir = %q", cfg.DownloadDir)
	}
	if cfg.Limit != 2048 {
		t.Errorf("Limit = %d", cfg.Limit)
	}
	if cfg.Proxy != "socks5://127.0.0.1:1080" {
		t.Errorf("Proxy = %q", cfg.Proxy)
	}
	if len(cfg.Headers) != 2 || len(cfg.Mirrors) != 2 {
		t.Errorf("Headers/Mirrors = %d/%d", len(cfg.Headers), len(cfg.Mirrors))
	}
	if cfg.S3.AccessKey != "ak" || cfg.S3.SecretKey != "sk" || cfg.S3.Bucket != "demo" {
		t.Errorf("S3 config = %+v", cfg.S3)
	}
	if cfg.SFTP.Host != "sftp.example.com" || cfg.SFTP.Port != 2222 || !cfg.SFTP.Insecure {
		t.Errorf("SFTP config = %+v", cfg.SFTP)
	}
	if cfg.Listen.Port != 16900 {
		t.Errorf("Listen config = %+v", cfg.Listen)
	}
	if cfg.configS3Options().SessionToken != "token" {
		t.Errorf("S3 session token not mapped")
	}
}

func TestLoadConfigMissingDefault(t *testing.T) {
	t.Setenv("LIFT_CONFIG", "")
	cfg, err := loadConfigFrom(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("missing default config should not fail: %v", err)
	}
	if cfg == nil || cfg.DownloadDir != "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfigRejectsInvalidHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"headers": ["invalid-header"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigFrom(path); err == nil {
		t.Fatal("invalid header should fail config loading")
	}
}

func TestConfigPriorityHelpers(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	output := fs.String("o", "", "")
	limit := fs.Int("limit", 0, "")
	proxy := fs.String("proxy", "", "")
	insecure := fs.Bool("insecure", false, "")

	if err := fs.Parse([]string{"-limit", "7", "http://example.com/file"}); err != nil {
		t.Fatal(err)
	}
	visited := visitedFlags(fs)

	applyIntDefault(limit, visited, "limit", 999)
	applyStringDefault(output, visited, "o", "/config/dir")
	applyStringDefault(proxy, visited, "proxy", "socks5://config")
	applyBoolDefault(insecure, visited, "insecure", true)

	if *limit != 7 {
		t.Fatalf("CLI limit should win, got %d", *limit)
	}
	if *output != "/config/dir" {
		t.Fatalf("config output should be applied, got %q", *output)
	}
	if *proxy != "socks5://config" {
		t.Fatalf("config proxy should be applied, got %q", *proxy)
	}
	if !*insecure {
		t.Fatal("config insecure should be applied")
	}
}

func TestHeadersFromLines(t *testing.T) {
	headers, err := headersFromLines([]string{"Cookie: a=b", "X-Test: one", "X-Test: two"})
	if err != nil {
		t.Fatalf("headersFromLines failed: %v", err)
	}
	if got := headers.Get("Cookie"); got != "a=b" {
		t.Errorf("Cookie = %q", got)
	}
	values := headers.Values("X-Test")
	if len(values) != 2 || values[0] != "one" || values[1] != "two" {
		t.Errorf("X-Test values = %v", values)
	}
	if _, err := headersFromLines([]string{"no-colon"}); err == nil {
		t.Fatal("missing colon should fail")
	}
	if _, err := headersFromLines([]string{"Bad Name: value"}); err == nil {
		t.Fatal("invalid header name should fail")
	}
}
