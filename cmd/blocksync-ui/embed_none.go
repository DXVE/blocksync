//go:build !embedcli

// 未启用 embedcli 构建标签时不含内置主程序，无法执行同步。
package main

var embeddedCLI []byte
