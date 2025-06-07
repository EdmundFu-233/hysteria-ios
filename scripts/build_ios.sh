#!/usr/bin/env bash
set -e
cd "$(dirname "$0")/.."

# 1. 静态编译 libhysteria 为 xcframework
gomobile bind -target=ios \
  -ldflags="-w -s" \
  -tags "ios" \
  -o ios/HysteriaKit.xcframework \
  libhysteria

# 2. Xcode 自动化构建（可选 fastlane）

