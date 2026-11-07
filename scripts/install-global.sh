#!/usr/bin/env bash
# lift 全局安装脚本：从 GitHub 下载最新发布版并安装到系统目录。
set -euo pipefail

REPO="LiStudioorg/lift"
BIN_NAME="lift"
INSTALL_DIR="/usr/local/bin"
INSTALL_PATH="$INSTALL_DIR/$BIN_NAME"
API_URL="${LIFT_API_URL:-https://api.github.com/repos/${REPO}/releases/latest}"
DOWNLOAD_BASE="${LIFT_DOWNLOAD_BASE:-https://github.com/${REPO}/releases/download}"
FORCE=0
TMP_DIR=""
TMP_TARGET=""

usage() {
  cat <<'EOF'
用法: sudo scripts/install-global.sh [-u|--upgrade]

选项:
  -u, --upgrade   即使已是最新版本也重新下载并覆盖安装
  -h, --help      显示帮助

安装位置: /usr/local/bin/lift

环境变量:
  LIFT_API_URL        覆盖 GitHub API 地址
  LIFT_DOWNLOAD_BASE  覆盖发布包下载基础地址
  GITHUB_TOKEN        可选，用于避免 GitHub API 限流
EOF
}

cleanup() {
  if [[ -n "$TMP_TARGET" ]]; then
    rm -f "$TMP_TARGET"
  fi
  if [[ -n "$TMP_DIR" ]]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT HUP INT TERM

while [[ $# -gt 0 ]]; do
  case "$1" in
    -u|--upgrade)
      FORCE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      if [[ $# -gt 0 ]]; then
        echo "错误: 不接受位置参数: $*" >&2
        exit 2
      fi
      ;;
    -*)
      echo "错误: 未知选项: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      echo "错误: 不接受位置参数: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  echo "错误: 全局安装需要 root 权限，请使用 sudo 运行。" >&2
  exit 1
fi

for cmd in curl tar awk mktemp; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "错误: 缺少命令: $cmd" >&2
    exit 1
  fi
done

case "$(uname -s)" in
  Linux)
    GOOS=linux
    ;;
  Darwin)
    GOOS=darwin
    ;;
  FreeBSD)
    GOOS=freebsd
    ;;
  *)
    echo "错误: 当前系统 $(uname -s) 不受此脚本支持。" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64)
    GOARCH=amd64
    ;;
  aarch64|arm64)
    GOARCH=arm64
    ;;
  armv8l|armv7l|armv6l|arm)
    GOARCH=arm
    ;;
  i386|i486|i586|i686|x86)
    GOARCH=386
    ;;
  mips)
    GOARCH=mips
    ;;
  mipsel)
    GOARCH=mipsle
    ;;
  mips64)
    GOARCH=mips64
    ;;
  mips64el)
    GOARCH=mips64le
    ;;
  ppc64)
    GOARCH=ppc64
    ;;
  ppc64le)
    GOARCH=ppc64le
    ;;
  s390x)
    GOARCH=s390x
    ;;
  *)
    echo "错误: 不支持的处理器架构: $(uname -m)" >&2
    exit 1
    ;;
esac

fetch_latest() {
  local response
  local curl_args=(
    -fsSL
    --retry 3
    --retry-delay 2
    --connect-timeout 15
    -H "Accept: application/vnd.github+json"
    -H "User-Agent: lift-installer"
  )
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    curl_args+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  fi
  response="$(curl "${curl_args[@]}" "$API_URL")" || return 1
  printf '%s\n' "$response" | awk -F'"' '/"tag_name"/ { print $4; exit }'
}

current_version() {
  if [[ -x "$INSTALL_PATH" ]]; then
    "$INSTALL_PATH" -v 2>/dev/null | awk '{ print $2; exit }' || true
  fi
}

LATEST="$(fetch_latest || true)"
if [[ -z "$LATEST" ]]; then
  echo "错误: 无法获取 GitHub 最新版本，请检查网络连接。" >&2
  exit 1
fi

CURRENT="$(current_version)"
if [[ "$CURRENT" == "$LATEST" && "$FORCE" -eq 0 ]]; then
  echo "当前已是最新版本 $LATEST，无需安装。"
  echo "如需强制重新安装，请使用 -u 或 --upgrade。"
  exit 0
fi

VERSION="${LATEST#v}"
ASSET="${BIN_NAME}_${VERSION}_${GOOS}_${GOARCH}.tar.gz"
DOWNLOAD_URL="${DOWNLOAD_BASE}/${LATEST}/${ASSET}"

if [[ -n "$CURRENT" ]]; then
  echo "当前版本: $CURRENT"
  echo "准备升级到: $LATEST"
else
  echo "未检测到已安装版本，准备安装: $LATEST"
fi

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/lift-install.XXXXXX")"
ARCHIVE="$TMP_DIR/$ASSET"
echo "正在下载 $ASSET ..."
if ! curl -fL -sS --retry 3 --retry-delay 2 --connect-timeout 15 -o "$ARCHIVE" "$DOWNLOAD_URL"; then
  echo "错误: 下载失败: $DOWNLOAD_URL" >&2
  echo "请确认该平台存在发布产物，或检查网络连接。" >&2
  exit 1
fi

if ! tar -xzf "$ARCHIVE" -C "$TMP_DIR" "$BIN_NAME"; then
  echo "错误: 解压失败: $ASSET" >&2
  exit 1
fi
if [[ ! -f "$TMP_DIR/$BIN_NAME" ]]; then
  echo "错误: 归档中未找到 $BIN_NAME" >&2
  exit 1
fi

if ! mkdir -p "$INSTALL_DIR"; then
  echo "错误: 无法创建目录: $INSTALL_DIR" >&2
  exit 1
fi

TMP_TARGET="$INSTALL_DIR/.${BIN_NAME}.tmp.$$"
cp -f "$TMP_DIR/$BIN_NAME" "$TMP_TARGET"
chmod 0755 "$TMP_TARGET"
if ! mv -f "$TMP_TARGET" "$INSTALL_PATH"; then
  echo "错误: 无法写入: $INSTALL_PATH" >&2
  exit 1
fi
TMP_TARGET=""

echo "已安装 $BIN_NAME: $INSTALL_PATH"
if ! "$INSTALL_PATH" -v; then
  echo "错误: 安装完成但无法运行 $INSTALL_PATH" >&2
  exit 1
fi
