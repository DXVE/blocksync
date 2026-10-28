//go:build embedcli

// 构建时把命令行主程序嵌入界面程序，供单文件分发。
package main

import _ "embed"

//go:embed embedded/BlockSync-CLI.exe
var embeddedCLI []byte
