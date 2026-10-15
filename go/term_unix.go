//go:build !windows

// 终端控制（macOS / Linux）—— TUI 前端的最小依赖。
//
// 0.01 §5.4 要求：不引入 curses / whiptail / dialog / 任何外部库。
// 这里只用「ANSI 转义序列 + stty」，两样都是系统自带。
//
// 为什么用 stty 而不是裸 termios：Linux 与 macOS 的 Termios 结构体字段名不同
// （Iflag/Oflag 的位定义、Cflag 常量都不一样），裸 syscall 要写两份且极易出错；
// stty 两个平台都有，语义一致。
package main

import (
	"os"
	"os/exec"
)

// clearScreen 清屏 + 光标归位。
// ANSI 序列优先：`clear` 命令在受限环境（容器、精简系统）里可能不存在（0.01 §10.2）。
func clearScreen() { os.Stdout.WriteString("\033[2J\033[H") }

// isTerminal 判断 stdin 是不是终端（管道 / 重定向时为 false）。
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func stty(args ...string) error {
	if !isTerminal() {
		return nil
	}
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// termInit 进入单键即时读取模式：关行缓冲（icanon）+ 关回显（echo）。
// 否则单个按键不会立即送达，必须敲回车 —— 那就不是「按任意键」了。
func termInit() { _ = stty("-echo", "-icanon", "min", "1", "time", "0") }

// termRestore 恢复普通行输入模式（退出 TUI 前必须调，否则终端会一直"哑"着）。
func termRestore() { _ = stty("echo", "icanon", "min", "1", "time", "0") }

// termCooked 临时恢复行输入 —— 逐行提问时交给终端做行编辑与回显。
func termCooked() { _ = stty("echo", "icanon", "min", "1", "time", "0") }

// termRaw 回到单键模式。
func termRaw() { _ = stty("-echo", "-icanon", "min", "1", "time", "0") }
