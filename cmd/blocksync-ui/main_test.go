//go:build windows

package main

import "testing"

func TestMassDeleteHint(t *testing.T) {
	logText := "输入：A\n输出：B\n错误: 本次将删除 120 个文件（输入目录共 500 个），超过安全上限 100。请先确认方向与两个目录无误，再加 --allow-mass-delete 重新执行"
	got := massDeleteHint(logText)
	if got == "" {
		t.Fatal("应识别出超量删除提示")
	}
	if massDeleteHint("错误: 目录不存在: X") != "" {
		t.Fatal("普通错误不应触发超量删除提示")
	}
}
