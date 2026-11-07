#!/usr/bin/env bash
# 动态构建除 iOS 外的全部 Go 目标平台。
# 注意：本脚本用于本地或手动全平台构建，不参与 release 工作流；
# 发布由 GoReleaser 在 GitHub Actions 中完成。
# 先构建 6 个核心平台，再构建其余平台；单个平台失败或超时仅跳过，最后汇总。
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# 固定使用本机 Go 工具链，避免构建过程中尝试下载其他工具链。
export GOTOOLCHAIN=local

OUT_DIR="${OUT_DIR:-dist}"
VERSION="${VERSION:-v0.1.0}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILT_AT="${BUILT_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
BUILD_TIMEOUT="${BUILD_TIMEOUT:-900}"

if [[ "$OUT_DIR" != /* ]]; then
  OUT_DIR="$ROOT_DIR/$OUT_DIR"
fi
mkdir -p "$OUT_DIR"

BUILD_LOG="${BUILD_LOG:-$OUT_DIR/build-errors.log}"
: >"$BUILD_LOG"

LDFLAGS="-s -w"
LDFLAGS+=" -X github.com/LiStudioorg/lift/internal/version.Version=${VERSION}"
LDFLAGS+=" -X github.com/LiStudioorg/lift/internal/version.GitCommit=${GIT_COMMIT}"
LDFLAGS+=" -X github.com/LiStudioorg/lift/internal/version.BuiltAt=${BUILT_AT}"

failed=()
built=0
total=0
core_failed=0

CORE_TARGETS=(
  linux/amd64
  linux/arm64
  darwin/amd64
  darwin/arm64
  windows/amd64
  windows/arm64
)

declare -A CORE_SET=()
for target in "${CORE_TARGETS[@]}"; do
  CORE_SET["$target"]=1
done

build_target() {
  local goos="$1"
  local goarch="$2"
  local output="$OUT_DIR/lift_${goos}_${goarch}"

  if [[ "$goos" == "windows" ]]; then
    output+=".exe"
  elif [[ "$goarch" == "wasm" ]]; then
    output+=".wasm"
  fi

  rm -f "$output"
  total=$((total + 1))
  printf '正在编译 %s/%s... ' "$goos" "$goarch"
  printf '\n===== %s/%s =====\n' "$goos" "$goarch" >>"$BUILD_LOG"

  local status=0
  if command -v timeout >/dev/null 2>&1; then
    timeout "$BUILD_TIMEOUT" env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$output" . >>"$BUILD_LOG" 2>&1 || status=$?
  else
    env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$output" . >>"$BUILD_LOG" 2>&1 || status=$?
  fi

  if [[ $status -eq 0 && -s "$output" ]]; then
    echo "成功"
    built=$((built + 1))
    return 0
  fi

  rm -f "$output"
  failed+=("${goos}/${goarch} (退出码 ${status})")
  echo "跳过该平台"
  return 1
}

echo "== 第一阶段：核心平台 =="
for target in "${CORE_TARGETS[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  if ! build_target "$goos" "$goarch"; then
    core_failed=$((core_failed + 1))
  fi
done

if [[ $core_failed -gt 0 ]]; then
  echo "警告: 有 ${core_failed} 个核心平台构建失败，继续构建其余平台以收集完整结果。" >&2
fi

echo
echo "== 第二阶段：其余平台 =="
while IFS= read -r target; do
  [[ -z "$target" ]] && continue
  if [[ -n "${CORE_SET["$target"]+x}" ]]; then
    continue
  fi
  goos="${target%/*}"
  goarch="${target#*/}"
  build_target "$goos" "$goarch" || true
done < <(go tool dist list | grep -v '^ios/' | sort)

echo
echo "构建完成：成功 ${built}/${total} 个平台。"
if [[ ${#failed[@]} -gt 0 ]]; then
  echo "失败平台（已跳过）:"
  for item in "${failed[@]}"; do
    echo "  - ${item}"
  done
else
  echo "没有失败平台。"
fi
echo "详细错误日志: ${BUILD_LOG}"
du -sh "$OUT_DIR"

if [[ $core_failed -gt 0 ]]; then
  exit 1
fi
exit 0
