#!/bin/sh
# Builds the release archives into dist/: one per Linux architecture, each
# holding the binary plus what the README's setup needs next to it. Asset names
# carry no version so releases/latest/download/<name> is a stable URL.
set -eu
version=${1:?usage: scripts/dist.sh <version, e.g. v1.2.0>}
out=dist
rm -rf "$out"
mkdir -p "$out"
for arch in amd64 arm64; do
	name=xui-remote_linux_$arch
	dir=$out/$name
	mkdir -p "$dir"
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$dir/xui-remote" .
	cp README.md LICENSE config.example.yaml xui-remote.service "$dir/"
	tar -C "$out" -czf "$out/$name.tar.gz" "$name"
	rm -rf "$dir"
done
cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum *.tar.gz >checksums.txt
else
	shasum -a 256 *.tar.gz >checksums.txt
fi
