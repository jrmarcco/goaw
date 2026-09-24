#!/usr/bin/env bash
set -euo pipefail

# tools for vscode go extension
echo "🚀 install & update gopls ..."
go install golang.org/x/tools/gopls@latest
echo "✅ done"

echo "🚀 install & update dlv ..."
go install github.com/go-delve/delve/cmd/dlv@latest
echo "✅ done"

echo "🚀 install & update goimports ..."
go install golang.org/x/tools/cmd/goimports@latest
echo "✅ done"

echo "🚀 install & update gofumpt ..."
go install mvdan.cc/gofumpt@latest
echo "✅ done"

echo "🚀 install & update golangci-lint ..."
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
echo "✅ done"

echo "🎉 setup tools complete"
