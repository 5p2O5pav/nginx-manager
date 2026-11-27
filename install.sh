#!/bin/sh
set -eu

if [ "$(uname -s)" != Linux ]; then
  echo '此安装脚本仅支持 Linux。' >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64)
    arch=amd64
    checksum='ac550463890c6887fe6568e13a24717b4edb735d3e189acccecea68dc03fb5fb'
    ;;
  aarch64|arm64)
    arch=arm64
    checksum='8caef9dae9248c04dc6f60ac9fc699f61e93a2dec0d80b3430a3eb072d783fa7'
    ;;
  *)
    echo "不支持的处理器架构：$(uname -m)，仅支持 amd64 和 arm64。" >&2
    exit 1
    ;;
esac

binary="nm-linux-$arch"
url="https://github.com/5p2O5pav/nginx-manager/releases/download/v1.0.0/$binary.tar.gz"

for command in curl tar sha256sum install mktemp; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "缺少命令：$command，请先安装后重试。" >&2
    exit 1
  fi
done

if [ "$(id -u)" -ne 0 ] && ! command -v sudo >/dev/null 2>&1; then
  echo '安装需要 root 权限，请以 root 运行或先安装 sudo。' >&2
  exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM
echo "正在下载 Nginx Manager v1.0.0 ($arch)..."
curl -fL --retry 3 "$url" -o "$tmpdir/$binary.tar.gz"
printf '%s  %s\n' "$checksum" "$tmpdir/$binary.tar.gz" | sha256sum -c -
tar -xzf "$tmpdir/$binary.tar.gz" -C "$tmpdir"

if [ "$(id -u)" -eq 0 ]; then
  install -m 0755 "$tmpdir/$binary" /usr/local/bin/nm
else
  sudo install -m 0755 "$tmpdir/$binary" /usr/local/bin/nm
fi

echo '安装完成！请运行 sudo nm 启动。'
