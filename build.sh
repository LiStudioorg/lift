#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${OUT_DIR:-dist}"
mkdir -p "$OUT_DIR"

# 支持的平台与架构：linux/darwin/windows × amd64/arm64
targets=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
  "windows amd64"
  "windows arm64"
)

for target in "${targets[@]}"; do
  read -r goos goarch <<< "$target"
  output="$OUT_DIR/lift-$goos-$goarch"
  if [ "$goos" = "windows" ]; then
    output="$output.exe"
  fi

  echo "构建 $output"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w" -o "$output" .
done

echo "全部构建完成，产物位于 $OUT_DIR/"
