# lift

`lift` 是一个使用 Go 编写的单二进制下载工具，提供命令行下载与轻量 HTTP API 两种使用方式。它支持 HTTP/HTTPS 分片断点续传、批量任务、镜像回退、自定义请求头、限速、SHA256 校验，以及 S3、SFTP、Docker 镜像和 BitTorrent 磁力链下载。

构建过程使用 `CGO_ENABLED=0`，不依赖系统动态库。配置文件采用 TOML，默认读取 `~/.lift/config.toml`。

## 目录

- [核心能力](#核心能力)
- [支持矩阵](#支持矩阵)
- [环境要求](#环境要求)
- [安装](#安装)
- [快速开始](#快速开始)
- [命令行参考](#命令行参考)
- [配置](#配置)
- [协议与使用](#协议与使用)
- [监听 API](#监听-api)
- [断点续传与状态文件](#断点续传与状态文件)
- [构建](#构建)
- [发布与版本](#发布与版本)
- [项目结构](#项目结构)
- [测试](#测试)
- [故障排查](#故障排查)
- [安全](#安全)
- [许可证](#许可证)

## 核心能力

- **HTTP/HTTPS**：HEAD 探测、4 MB 分片、并发传输、断点续传、镜像回退
- **批量下载**：命令行 URL 或列表文件，固定并发 worker，统一汇总结果
- **数据完整性**：下载完成后校验 SHA256；断点状态通过 ETag 与文件大小校验
- **访问控制**：自定义请求头、HTTP/SOCKS5 代理、全局限速
- **多协议**：S3 兼容存储、SFTP、Docker 镜像导出、BitTorrent 磁力链
- **本地 API**：提交下载任务、查询任务状态、健康检查
- **配置管理**：TOML 配置、命令行覆盖、缺失配置降级为空配置
- **版本注入**：版本号、Git 提交、构建时间在编译期写入二进制
- **单二进制交付**：默认关闭 CGO，便于跨平台分发

## 支持矩阵

### 下载协议

| 协议 | 入口 | 说明 |
| --- | --- | --- |
| HTTP/HTTPS | `lift <url>` | 分片、断点续传、请求头、代理、限速、镜像回退 |
| S3 | `s3://bucket/key` 或 `lift s3 pull` | 兼容 S3 的对象存储，支持临时令牌与区域 |
| SFTP | `sftp://user:pass@host/path` 或 `lift sftp pull` | 密码或私钥认证，支持 known_hosts 校验 |
| Docker | `lift docker pull` | 导出镜像为 tar，使用 Docker 默认凭据链 |
| BitTorrent | `lift magnet` | 磁力链、DHT、Tracker、断点续传 |

### 构建与发布目标

`scripts/build.sh` 会遍历 `go tool dist list` 的全部目标（排除 `ios/`），用于本地或手动全平台验证。发布由 GoReleaser 完成，发布矩阵覆盖以下已验证目标：

| 系统 | 架构 |
| --- | --- |
| Linux | `386`、`amd64`、`arm`、`arm64`、`mips`、`mipsle`、`mips64`、`mips64le`、`ppc64`、`ppc64le`、`s390x` |
| macOS | `amd64`、`arm64` |
| Windows | `386`、`amd64`、`arm64` |
| FreeBSD | `386`、`amd64`、`arm`、`arm64`、`riscv64` |
| WebAssembly | `js/wasm` |

依赖链无法在 `CGO_ENABLED=0` 下编译的目标会在 GoReleaser 配置中排除；`scripts/build.sh` 仍会尝试全部目标，并在结束时汇总失败平台。

## 环境要求

| 项目 | 要求 |
| --- | --- |
| Go | 1.27.1 或更高版本（以 `go.mod` 为准） |
| 操作系统 | Linux、macOS、Windows、FreeBSD 等 Go 支持的系统 |
| 构建工具 | `go`、`git`；全平台脚本还需要 Bash 与 `timeout` |
| 运行权限 | 普通用户即可；写入系统目录时按需提升权限 |

## 安装

### 一键安装脚本

仓库提供四个独立安装脚本。脚本会查询 GitHub 最新 Release，根据当前系统和架构下载对应产物；已安装版本与最新版本一致时跳过，使用 `-u` 或 `--upgrade` 可强制重新安装并覆盖旧文件。

#### Linux / macOS

全局安装到 `/usr/local/bin/lift`，需要 root 权限：

```bash
sudo ./scripts/install-global.sh
sudo ./scripts/install-global.sh -u
```

用户安装到 `~/.local/bin/lift`：

```bash
./scripts/install-user.sh
./scripts/install-user.sh -u
```

如果 `~/.local/bin` 不在 `PATH` 中，`install-user.sh` 会输出添加方式：

```bash
export PATH="$HOME/.local/bin:$PATH"
```

也可以从 GitHub 直接执行：

```bash
curl -fsSL https://raw.githubusercontent.com/LiStudioorg/lift/main/scripts/install-global.sh | sudo bash
curl -fsSL https://raw.githubusercontent.com/LiStudioorg/lift/main/scripts/install-user.sh | bash
```

强制重新安装时追加参数：

```bash
curl -fsSL https://raw.githubusercontent.com/LiStudioorg/lift/main/scripts/install-global.sh | sudo bash -s -- -u
curl -fsSL https://raw.githubusercontent.com/LiStudioorg/lift/main/scripts/install-user.sh | bash -s -- -u
```

#### Windows

以管理员身份运行 `scripts\install-global.bat`，安装到 `C:\ProgramData\lift`，并通过 `setx /M` 写入系统 PATH。

普通用户运行 `scripts\install-user.bat`，安装到 `%LOCALAPPDATA%\lift`，并通过 `setx` 写入当前用户 PATH。

两个脚本都支持 `-u` 或 `--upgrade` 强制重新安装。安装完成后需要打开新的终端，使 PATH 变更生效。

### 使用发布产物

从 GitHub Releases 下载对应平台的压缩包，解压后直接运行：

```bash
tar -xzf lift_<version>_<os>_<arch>.tar.gz
./lift -v
```

Windows 发布包为 `.zip`，解压后得到 `lift.exe`。

### 从源码构建

```bash
git clone git@github.com:LiStudioorg/lift.git
cd lift
go build -trimpath -o lift .
./lift -v
```

默认版本为 `v0.1.0`。发布构建会通过链接参数写入实际版本、提交号和构建时间。

## 快速开始

### 单文件下载

```bash
./lift https://example.com/releases/file.bin
./lift https://example.com/releases/file.bin -o ./downloads/file.bin
```

未指定 `-o` 时，输出文件名从 URL 路径推导，并只保留最后一段，避免目录穿越。无法推导时使用 `download-<长度>.bin`。

### 断点续传

中断后使用相同命令重新运行：

```bash
./lift https://example.com/releases/file.bin -o file.bin
```

下载过程中会生成 `file.bin.lift` 状态文件。远端文件大小和 ETag 未变化时自动续传；变化时重新开始。下载成功后会删除状态文件。

### 批量下载

```bash
./lift -o ./downloads https://example.com/a.bin https://example.com/b.bin
./lift -f urls.txt -o ./downloads
```

`urls.txt` 每行一个 URL，空行和以 `#` 开头的行会被忽略。批量模式下 `-o` 表示输出目录。

### 限速、代理与校验

```bash
# 限速 1024 KB/s
./lift https://example.com/file.bin -limit 1024

# HTTP 代理或 SOCKS5 代理
./lift https://example.com/file.bin -proxy http://127.0.0.1:8080
./lift https://example.com/file.bin -proxy socks5://127.0.0.1:1080

# 下载完成后校验 SHA256（仅支持单文件任务）
./lift https://example.com/file.bin -sha256 <sha256>
```

### 自定义请求头

```bash
./lift https://example.com/file.bin \
  -H "Cookie: session=xxx" \
  -H "Referer: https://example.com/page" \
  -H "User-Agent: lift/0.1" \
  -o file.bin
```

`-H` 可以重复指定。请求头会附加到 HEAD、GET、Range 续传请求以及镜像回退请求。

### 镜像回退

```bash
./lift https://github.com/user/repo/releases/download/v1/file.bin \
  -mirror 'https://ghproxy.com/{url},https://mirror.example/fetch?u={url_encoded}' \
  -o file.bin
```

模板占位符：

| 占位符 | 含义 |
| --- | --- |
| `{url}` | 原始 URL 原样填入 |
| `{url_encoded}` | 原始 URL 经过 URL 编码后填入 |

模板不含占位符时按前缀处理，直接拼接原始 URL。主 URL 返回错误状态、连接失败或超时后，按顺序尝试镜像；断点续传状态仍以主 URL 对应的文件为标识。

## 命令行参考

### 根命令

```text
lift [选项] <URL> [URL...]
lift docker pull <image> [-o output.tar]
lift s3 pull <key> --bucket <bucket> [选项]
lift sftp pull <path> --host <host[:port]> --user <user> [选项]
lift magnet <magnet_uri> [-o output_dir]
lift listen [-host 127.0.0.1] [-port 16800] [选项]
lift upgrade [-f|--force]
```

### 全局选项

| 选项 | 说明 |
| --- | --- |
| `-o <path>` | 输出文件；批量下载时作为输出目录 |
| `-limit <KB/s>` | 全局限速，`0` 表示不限速 |
| `-f <file>` | URL 列表文件，每行一个，忽略空行与 `#` 注释 |
| `-sha256 <hash>` | 下载完成后校验 SHA256，仅支持单文件任务 |
| `-proxy <url>` | 代理地址，支持 `http://` 与 `socks5://` |
| `-mirror <templates>` | 逗号分隔的镜像模板 |
| `-H <header>` | 自定义请求头，可重复指定 |
| `-s3-endpoint <url>` | S3 端点；留空使用 AWS 默认端点 |
| `-s3-ak <key>` | S3 Access Key |
| `-s3-sk <secret>` | S3 Secret Key |
| `-s3-token <token>` | S3 Session Token |
| `-s3-region <region>` | S3 Region |
| `-sftp-user <user>` | SFTP 用户名 |
| `-sftp-pass <password>` | SFTP 密码 |
| `-sftp-key <path>` | SFTP 私钥路径 |
| `-sftp-key-pass <password>` | SFTP 私钥密码 |
| `-sftp-known-hosts <path>` | SFTP known_hosts 路径 |
| `-sftp-insecure` | 跳过 SFTP 主机密钥校验，不推荐 |
| `-v`, `--version` | 显示版本 |
| `-h`, `--help` | 显示帮助 |

选项可以出现在 URL 前后；解析阶段会将选项与位置参数重新排序。

### 退出码

| 退出码 | 含义 |
| --- | --- |
| `0` | 成功 |
| `1` | 下载失败、校验失败或服务异常 |
| `2` | 参数错误或配置错误 |
| `130` | 收到中断信号，任务已取消 |

## 配置

### 配置路径

默认路径：

```text
~/.lift/config.toml
```

可通过环境变量覆盖：

```bash
LIFT_CONFIG=/etc/lift/config.toml ./lift https://example.com/file.bin
```

默认配置文件不存在时按空配置处理，不生成运行时配置。显式设置 `LIFT_CONFIG` 但文件不存在时返回配置错误。

### 优先级

```text
命令行显式参数 > 配置文件 > 内置默认值
```

只有命令行中实际出现的选项才会覆盖配置。例如显式传入 `-limit 0` 表示本次关闭限速，而不是回退到配置文件中的限速值。

### 完整示例

仓库中的 [`configs/config.example.toml`](configs/config.example.toml) 保留全部配置键，值均为空字符串、`0`、`false` 或空数组。复制到默认路径后按需填写：

```toml
download_dir = "~/Downloads/lift"
limit = 2048
proxy = "socks5://127.0.0.1:1080"
headers = ["Cookie: session=xxx", "Referer: https://example.com/"]
mirrors = ["https://ghproxy.com/{url}"]

[s3]
endpoint = "https://s3.amazonaws.com"
bucket = "my-bucket"
access_key = "AK"
secret_key = "SK"
session_token = ""
region = "us-east-1"

[sftp]
host = "sftp.example.com"
port = 22
user = "demo"
password = ""
private_key = "~/.ssh/id_ed25519"
private_key_passphrase = ""
known_hosts = "~/.ssh/known_hosts"
insecure = false

[listen]
host = "127.0.0.1"
port = 16800
```

### 字段说明

顶层字段：

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `download_dir` | string | 空 | 默认下载目录；空表示当前目录 |
| `limit` | int | `0` | 限速，单位 KB/s；`0` 表示不限速 |
| `proxy` | string | 空 | HTTP 或 SOCKS5 代理 |
| `headers` | array | 空 | 请求头列表，格式为 `"Name: value"` |
| `mirrors` | array | 空 | HTTP 镜像模板 |

`[s3]`：

| 字段 | 说明 |
| --- | --- |
| `endpoint` | S3 端点；留空使用 `https://s3.amazonaws.com` |
| `bucket` | 默认存储桶 |
| `access_key` | Access Key |
| `secret_key` | Secret Key |
| `session_token` | 临时令牌，可选 |
| `region` | 区域，可选 |

`[sftp]`：

| 字段 | 说明 |
| --- | --- |
| `host` | 主机名，可包含端口 |
| `port` | 端口；`0` 表示默认 `22` |
| `user` | 用户名 |
| `password` | 密码 |
| `private_key` | 私钥路径 |
| `private_key_passphrase` | 私钥密码 |
| `known_hosts` | known_hosts 路径 |
| `insecure` | 跳过主机密钥校验，不推荐 |

`[listen]`：

| 字段 | 说明 |
| --- | --- |
| `host` | 监听地址，默认 `127.0.0.1` |
| `port` | 监听端口，默认 `16800` |

配置文件中的 `download_dir` 会作为默认输出目录：

- 单文件下载：`<download_dir>/<文件名>`
- 批量下载：作为输出目录
- `s3 pull`、`sftp pull`、`docker pull`：未指定 `-o` 时使用
- `magnet`：未指定 `-o` 时作为 BT 输出目录
- `listen`：未指定 `-o` 时作为任务下载目录

配置中的 `headers` 必须符合 `Name: value` 格式，否则加载失败。未知 TOML 字段会在错误输出中提示并忽略。

## 协议与使用

### S3

URL 路由：

```bash
./lift s3://my-bucket/path/file.bin \
  --s3-endpoint https://s3.amazonaws.com \
  --s3-ak AK --s3-sk SK -o file.bin
```

子命令：

```bash
./lift s3 pull path/file.bin \
  --bucket my-bucket \
  --endpoint https://s3.amazonaws.com \
  --ak AK --sk SK \
  -o file.bin
```

未指定的 endpoint、bucket、Access Key、Secret Key、Session Token、Region 会从 `[s3]` 配置读取。Access Key 与 Secret Key 必须同时提供。

### SFTP

URL 路由：

```bash
./lift sftp://user:pass@host:22/path/file.bin -o file.bin
```

子命令与私钥认证：

```bash
./lift sftp pull /path/file.bin \
  --host host --port 22 --user user \
  --key ~/.ssh/id_ed25519 \
  -o file.bin
```

默认使用 `known_hosts` 校验主机密钥。URL 路由模式可用 `--sftp-insecure`，`sftp pull` 子命令可用 `--insecure` 跳过校验，仅建议在隔离测试环境使用。

### Docker

```bash
./lift docker pull alpine:latest -o alpine.tar
./lift docker pull registry.example.com/team/app:1.0 -o app.tar -limit 1024
```

认证使用 Docker 默认凭据链。输出为 tar 归档，可交给 `docker load` 使用。未指定 `-o` 时，文件名根据镜像名推导，并写入配置的下载目录。

### BitTorrent

```bash
./lift magnet 'magnet:?xt=urn:btih:...' -o ./downloads
```

可选参数：

| 参数 | 说明 |
| --- | --- |
| `-limit <KB/s>` | 下载限速 |
| `-port <port>` | BT 监听端口，`0` 表示随机端口 |
| `-no-dht` | 禁用 DHT，仅使用磁力链中的 Tracker |

运行期间输出进度、Peer 数、种子数、速度与预计剩余时间。中断后重新运行相同命令，会复用已完成的 Piece 继续下载。

## 监听 API

启动服务：

```bash
./lift listen -port 16800
./lift listen -host 0.0.0.0 -port 16800 -o ./downloads -limit 1024 -proxy socks5://127.0.0.1:1080
```

默认仅监听 `127.0.0.1`，不提供身份认证。若绑定公网地址，应在反向代理层增加认证与访问控制。

### 提交任务

```bash
curl -X POST http://127.0.0.1:16800/api/download \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/file.bin"}'
```

可选 `output` 字段用于指定文件名；相对路径会落在下载目录下：

```json
{
  "url": "https://example.com/file.bin",
  "output": "renamed.bin"
}
```

成功返回 HTTP `202 Accepted`：

```json
{
  "id": 1,
  "url": "https://example.com/file.bin",
  "output": "/path/downloads/file.bin",
  "status": "queued",
  "created_at": "2026-11-07T12:00:00+08:00"
}
```

### 查询任务

```bash
# 全部任务
curl http://127.0.0.1:16800/api/tasks

# 单个任务
curl http://127.0.0.1:16800/api/tasks/1

# 健康检查
curl http://127.0.0.1:16800/api/health
```

任务状态：

| 状态 | 含义 |
| --- | --- |
| `queued` | 已入队，等待 worker |
| `running` | 正在下载 |
| `success` | 下载成功 |
| `failed` | 下载失败，`error` 字段包含原因 |
| `canceled` | 服务关闭时尚未完成的任务 |

健康检查返回示例：

```json
{
  "status": "ok",
  "queued": 1,
  "running": 2,
  "total": 3
}
```

服务使用固定 2 个下载 worker，任务队列长度为 128。API 支持 `http://`、`https://`、`s3://` 和 `sftp://` 地址；S3 与 SFTP 连接参数从配置文件读取。

## 断点续传与状态文件

- 分片大小为 4 MB
- 状态文件保存 URL、ETag、文件大小和分片进度
- 状态文件路径为 `<输出文件>.lift`
- 仅当 ETag 与文件大小同时匹配时才续传
- 状态文件损坏时重新开始下载，不中断任务
- 下载成功并完成可选的 SHA256 校验后删除状态文件
- 批量任务与 API 任务共用同一套状态机制

手动删除 `.lift` 文件会放弃已有进度。不要手工编辑状态文件。

## 构建

### 本机构建

```bash
go build -trimpath -o lift .
```

### 全平台构建脚本

```bash
./scripts/build.sh
```

脚本行为：

- 通过 `go tool dist list` 获取目标，排除 `ios/`
- 固定 `GOTOOLCHAIN=local`，避免构建过程中下载工具链
- 先构建 linux/darwin/windows 的 amd64/arm64 六个核心平台
- 再构建其余目标
- 单目标默认超时 15 分钟
- 单个目标失败或超时只打印“跳过该平台”，不中断整体流程
- 结束时输出成功数量、失败平台列表和产物大小
- 详细编译错误写入 `dist/build-errors.log`

可覆盖的环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `VERSION` | `v0.1.0` | 注入的版本号 |
| `GIT_COMMIT` | 当前短提交 | 注入的提交号 |
| `BUILT_AT` | 当前 UTC 时间 | 注入的构建时间 |
| `OUT_DIR` | `dist` | 产物目录 |
| `BUILD_TIMEOUT` | `900` | 单目标超时秒数 |
| `BUILD_LOG` | `dist/build-errors.log` | 详细错误日志 |

产物命名：

```text
lift_<GOOS>_<GOARCH>
```

Windows 追加 `.exe`，WebAssembly 追加 `.wasm`。

### 测试

```bash
go test ./...
```

测试覆盖请求头注入、镜像回退、配置解析与优先级、空配置处理、监听 API 下载流程。

## 发布与版本

版本信息由 `internal/version` 保存，默认值：

```text
Version   = v0.1.0
GitCommit = unknown
BuiltAt   = unknown
```

通过链接参数覆盖：

```bash
go build -trimpath \
  -ldflags "-s -w \
    -X github.com/LiStudioorg/lift/internal/version.Version=v0.1.0 \
    -X github.com/LiStudioorg/lift/internal/version.GitCommit=$(git rev-parse --short HEAD) \
    -X github.com/LiStudioorg/lift/internal/version.BuiltAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o lift .
```

查看版本：

```bash
./lift -v
./lift --version
```

输出示例：

```text
lift v0.1.0 (commit 1a2b3c4, built 2026-11-07T12:00:00Z)
```

### 自更新

`lift upgrade` 会查询 GitHub 最新 Release，比较当前版本；有新版本时下载当前平台的发布包并覆盖当前可执行文件。`-f` 或 `--force` 可强制重新安装。

```bash
./lift upgrade
./lift upgrade -f
```

Windows 下如果可执行文件正在运行，会先将旧文件重命名为 `.old`，写入新文件后再清理；如果旧文件仍被占用，会在下次升级时继续清理。

### 发布流程

仓库包含 `.goreleaser.yaml` 与 `.github/workflows/release.yml`：

- 推送 `v*` 标签触发 GitHub Actions
- 工作流检出完整 Git 历史
- 使用 `actions/setup-go@v5` 安装 `go.mod` 指定版本
- 使用 `goreleaser/goreleaser-action@v6` 执行 `release --clean`
- 生成多平台二进制、压缩包与 `checksums.txt`
- 通过标签版本写入二进制

示例：

```bash
git tag v0.1.0
git push origin v0.1.0
```

## 项目结构

```text
main.go                  根命令、全局参数与子命令路由
internal/cli             共享参数解析、下载完成与退出码处理
internal/config          TOML 配置加载、默认路径与命令行优先级
internal/core            下载引擎、分片状态、批量任务、限速与校验
internal/protocol        URL 协议路由与协议参数转换
internal/protocol/http   HTTP/HTTPS 源、请求头与镜像回退
internal/protocol/s3     S3 兼容存储源
internal/protocol/sftp   SFTP 源
internal/protocol/docker Docker 镜像导出
internal/server          监听 API 服务与任务队列
internal/torrent         BitTorrent 磁力链引擎
internal/upgrade         自更新命令
internal/version         构建期版本变量
scripts/build.sh         全目标构建与失败汇总脚本
scripts/install-global.sh  Linux/macOS 全局安装
scripts/install-user.sh    Linux/macOS 用户安装
scripts/install-global.bat Windows 全局安装
scripts/install-user.bat   Windows 用户安装
configs/                 配置示例
```

## 故障排查

| 现象 | 排查方向 |
| --- | --- |
| 配置加载失败 | 检查 TOML 语法；`headers` 必须为 `Name: value` |
| HTTP 403 或超时 | 增加 `-H` 请求头；配置 `-mirror`；检查代理 |
| 断点未生效 | 远端 ETag 或文件大小已变化；删除对应 `.lift` 后重试 |
| S3 访问被拒绝 | 检查 endpoint、region、Access Key、Secret Key 与桶权限 |
| SFTP 主机密钥错误 | 更新 `known_hosts`；测试环境才考虑 `insecure` |
| BT 长时间无速度 | 检查 Tracker、DHT、监听端口与防火墙 |
| 发布构建失败 | 查看 `dist/build-errors.log` 与 GoReleaser 日志 |
| 监听 API 无法访问 | 检查 `-host`、`-port` 与防火墙；公网访问需增加认证 |

## 安全

- 发布包提供 `checksums.txt`，下载后应校验文件完整性
- 支持 SHA256 校验的下载任务建议使用 `-sha256`
- 配置文件可能包含密钥和令牌，建议权限设置为 `0600`
- 不要把包含凭据的配置文件提交到版本库
- 代理和镜像会接触下载 URL，应使用可信服务
- 监听 API 默认无认证，只应绑定本机或受保护的内网地址
- SFTP 默认校验主机密钥，不要在生产环境关闭校验
- BitTorrent 内容来源不受控，下载后应自行校验与扫描

## 许可证

本项目使用 MIT License，详见 [LICENSE](LICENSE)。
