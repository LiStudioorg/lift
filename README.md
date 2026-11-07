# lift

`lift` 是一个纯 Go、单二进制的下载工具，支持 HTTP/HTTPS 分片断点续传、批量下载、Docker 镜像导出、S3、SFTP 和 BitTorrent 磁力链下载。全部平台均可使用 `CGO_ENABLED=0` 构建。

## 构建

```bash
go build -o lift .
# 或交叉编译六个平台
./build.sh
```

## 基础下载

```bash
# 单文件
./lift https://example.com/file.bin -o file.bin

# 断点续传：中断后使用相同命令重新运行
./lift https://example.com/file.bin -o file.bin

# 限速 1MB/s、SHA256 校验
./lift https://example.com/file.bin -o file.bin -limit 1024 -sha256 <sha256>

# 批量下载，-o 作为输出目录
./lift -o ./downloads https://example.com/a.bin https://example.com/b.bin
./lift -f urls.txt -o ./downloads

# 使用代理
./lift https://example.com/file.bin -proxy socks5://127.0.0.1:1080
```

## 自定义请求头

使用 `-H` 注入自定义请求头，可重复指定：

```bash
./lift https://example.com/file.bin \
  -H "Cookie: session=xxx" \
  -H "Referer: https://example.com/page" \
  -H "User-Agent: lift/1.0" \
  -o file.bin
```

请求头会同时附加到：

- `HEAD` 统计请求
- `GET` 下载请求（包括 `Range` 分片/续传请求）
- 镜像回退产生的请求

也可以写在配置文件的 `headers` 数组中。

## 镜像回退

`-mirror` 接受逗号分隔的镜像 URL 模板。当主 URL 返回 403、超时、连接失败或其他请求错误时，会按顺序尝试镜像：

```bash
./lift https://github.com/user/repo/releases/download/v1/file.bin \
  -mirror 'https://ghproxy.com/{url},https://mirror.example/fetch?u={url_encoded}' \
  -o file.bin
```

模板占位符：

- `{url}`：原始 URL 原样填入
- `{url_encoded}`：原始 URL 经过 URL 编码后填入
- 不含占位符时：模板作为前缀，直接拼接原始 URL

镜像机制仅作用于 HTTP/HTTPS 源。断点续传状态仍以主 URL 为标识。

## 配置文件

默认读取 `~/.lift/config.json`，也可用 `LIFT_CONFIG` 环境变量指定其他路径：

```bash
LIFT_CONFIG=/etc/lift/config.json ./lift https://example.com/file.bin
```

优先级：**命令行参数 > 配置文件 > 内置默认值**。只有命令行中显式出现的选项才会覆盖配置，例如显式传入 `-limit 0` 表示本次不限速。

示例：

```json
{
  "download_dir": "~/Downloads/lift",
  "limit": 2048,
  "proxy": "socks5://127.0.0.1:1080",
  "headers": [
    "Cookie: session=xxx",
    "Referer: https://example.com/"
  ],
  "mirrors": [
    "https://ghproxy.com/{url}",
    "https://mirror.example/fetch?u={url_encoded}"
  ],
  "s3": {
    "endpoint": "https://s3.amazonaws.com",
    "bucket": "my-bucket",
    "access_key": "AK",
    "secret_key": "SK",
    "session_token": "",
    "region": "us-east-1"
  },
  "sftp": {
    "host": "sftp.example.com",
    "port": 22,
    "user": "demo",
    "password": "password",
    "private_key": "~/.ssh/id_ed25519",
    "private_key_passphrase": "",
    "known_hosts": "~/.ssh/known_hosts",
    "insecure": false
  },
  "listen": {
    "host": "127.0.0.1",
    "port": 16800
  }
}
```

配置中的 `download_dir` 会作为默认下载目录：

- 单文件下载时保存为 `<download_dir>/<从 URL 推导的文件名>`
- 批量下载时作为输出目录
- `s3 pull`、`sftp pull`、`docker pull` 未指定 `-o` 时写入该目录
- `magnet` 未指定 `-o` 时作为 BT 输出目录
- `listen` 未指定 `-o` 时作为后台任务下载目录

## 浏览器接管（无 UI）

启动一个仅监听本机的轻量 HTTP API：

```bash
./lift listen -port 16800
# 可选：-host 0.0.0.0 -o ./downloads -limit 1024 -proxy ... -H ... -mirror ...
```

发送下载请求：

```bash
curl -X POST http://127.0.0.1:16800/api/download \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/file.bin"}'
```

可选 `output` 字段用于指定文件名（相对路径会落到下载目录）：

```json
{"url":"https://example.com/file.bin","output":"renamed.bin"}
```

响应示例（HTTP 202）：

```json
{
  "id": 1,
  "url": "https://example.com/file.bin",
  "output": "/path/downloads/file.bin",
  "status": "queued",
  "created_at": "2026-11-07T12:00:00+08:00"
}
```

任务会进入现有的后台下载队列，默认 2 个并发 worker：

```bash
# 查询全部任务
curl http://127.0.0.1:16800/api/tasks

# 查询单个任务
curl http://127.0.0.1:16800/api/tasks/1

# 健康检查
curl http://127.0.0.1:16800/api/health
```

任务状态为 `queued`、`running`、`success`、`failed` 或 `canceled`。默认只监听 `127.0.0.1`；若改为公网地址，请自行增加反向代理鉴权和访问控制。API 也支持 `s3://` 和 `sftp://` 地址，连接参数从配置文件读取。

## Docker

```bash
./lift docker pull alpine:latest -o alpine.tar -limit 1024
```

## S3

```bash
# URL 路由
./lift s3://my-bucket/path/file.bin \
  --s3-endpoint https://s3.amazonaws.com \
  --s3-ak AK --s3-sk SK -o file.bin

# 子命令
./lift s3 pull path/file.bin \
  --bucket my-bucket --endpoint https://s3.amazonaws.com \
  --ak AK --sk SK -o file.bin
```

未指定的 endpoint、AK、SK、bucket、region 会从配置文件读取。

## SFTP

```bash
# URL 路由（密码在 URL 中）
./lift sftp://user:pass@host:22/path/file.bin -o file.bin

# 子命令 + 私钥
./lift sftp pull /path/file.bin \
  --host host --port 22 --user user \
  --key ~/.ssh/id_ed25519 -o file.bin
```

默认校验 `known_hosts`；测试环境可显式使用 `--sftp-insecure` 跳过主机密钥校验。

## BitTorrent 磁力链

```bash
./lift magnet 'magnet:?xt=urn:btih:...' -o ./downloads
# 可选：-limit KB/s、-port 监听端口、-no-dht
```

每秒输出进度、Peers、种子数、速度和 ETA。中断后重新运行相同命令会利用已下载的 Piece 继续下载。

## 测试

```bash
go test ./...
```

仓库内测试覆盖请求头注入、镜像回退、配置解析与优先级、监听 API 下载流程。
