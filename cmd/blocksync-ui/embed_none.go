//go:build !embedcli

// 未启用 embedcli 构建标签时不含内置主程序，程序只使用外部 blocksync.exe。
package main

var embeddedCLI []byte
