# 仓库约束

改动的可执行约束与验收方式。设计背景见 [wiki/工作原理与性能](wiki/工作原理与性能.md)。

## 构建与验证

```bat
go test ./...
build.cmd
```

- `go test ./...` 需在 Windows 上运行：GUI 使用 Win32 调用，端到端用例以子进程执行 CLI。
- `build.cmd` 产出 `dist\BlockSync-CLI.exe` 与内嵌 CLI 的 `dist\BlockSync-GUI.exe`；手工编译 GUI 时须带 `-tags embedcli`，否则内嵌资源走空实现。
- `go vet ./...` 目前会在 `cmd/blocksync-ui` 报 `unsafe.Pointer` 告警，暂不作为合并门槛。

## 改哪里

同一事实只写一处，其余文档放链接：

| 主题 | 唯一位置 |
|---|---|
| 子命令、选项、输出格式、退出码 | `wiki/命令参考.md` |
| 界面进程协议、控件、配置对话框、日志区行为 | `wiki/图形界面.md` |
| 分块与哈希、索引格式、同步决策、写入路径、性能模型、已知限制 | `wiki/工作原理与性能.md` |
| 排除规则语法、默认规则、匹配算法 | `wiki/排除列表.md` |
| 根目录校验、句柄核验、确认、删除上限、运行期锁 | `wiki/源目录保护.md` |
| 演练脚本与部署步骤 | `wiki/演练与部署.md` |
| 提示语与排查步骤 | `wiki/常见问题.md` |

## 输出格式契约

`ConfirmMarker`、`DoneMarker`、`ProgressPattern`、`ResultPattern`（`internal/mirror`）是 CLI 与 GUI 之间的接口。改动任一输出行时同步三处：

1. `wiki/命令参考.md` 的「输出格式」一节；
2. 解析侧的常量与正则（`internal/mirror`、`cmd/blocksync-ui`）；
3. GUI 状态栏与确认框文案。

验收：`go test ./cmd/blocksync ./internal/mirror`，端到端用例会用这些模式解析真实输出。

## 索引格式

`internal/index` 的 `indexVersion` 与字节布局、哈希算法、块参数语义绑定；其中任一项变化都要 bump 版本，旧索引随后按损坏处理并重扫。

## 版本号

唯一来源是 `internal/version` 的 `Version`；发版 tag 与它比较时去掉 `v` 前缀，不要在其他位置复制版本号。
