#!/usr/bin/env bash
set -euo pipefail

set -a
source .env
set +a

go run cmd/main.go -prompt="我需要你搭建一个极简的 Go 语言 Web Server 项目。"
