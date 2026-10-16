//go:build !windows

// 终端控制（macOS / Linux）—— TUI 前端的最小依赖。
//
// 0.01 §5.4 要求：不引入 curses / whiptail / dialog / 任何外部库。
// 这里只用「ANSI 转义序列 + stty」，两样都是系统自带。
//
// 为什么用 stty 而不是裸 termios：Linux 与 macOS 的 Termios 结构体字段名不同
// （Iflag/Oflag 的位定义、Cflag 常量都不一样），裸 syscall 要写两份且极易出错；
// stty 两个平台都有，语义一致。
//
// **不再来回切行输入模式**：之前逐行提问时会临时切回 cooked，结果行尾是 '\r' 还是 '\n'
// 取决于切换与读请求的先后，会出现「敲了没反应」。现在全程单键模式，
// 行编辑（回显、退格）由本程序自己做（见 input.go）。
package main

import (
	"os"
	"os/exec"
)

// clearScreen 清屏 + 光标归位。
// ANSI 序列优先：`clear` 命令在受限环境（容器、精简系统）里可能不存在（0.01 §10.2）。
func clearScreen() { _, _ = os.Stdout.WriteString("\033[2J\033[H") }

// isTerminal 判断 stdin 是不是终端（管道 / 重定向时为 false）。
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

var (
	rawOK   bool
	echoOff bool
)

// stty 执行一条 stty（非终端时直接跳过，避免污染 stdin）。
func stty(args ...string) bool {
	if !isTerminal() {
		return false
	}
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run() == nil
}

// termInit 进入单键模式：关行缓冲（-icanon）+ 关回显（-echo）。
func termInit() {
	if stty("-echo", "-icanon", "min", "1", "time", "0") {
		rawOK = true
		echoOff = true // stty -echo 成功即终端不再回显，由本程序回显
	}
}

// termRestore 恢复普通行输入模式（退出 TUI 前必须调，否则终端会一直"哑"着）。
func termRestore() { stty("echo", "icanon", "min", "1", "time", "0") }

// singleKeyOK 单键模式是否真的生效（stdin 不是终端时为 false）。
func singleKeyOK() bool { return rawOK }

// needSelfEcho 是否需要本程序自己回显。
func needSelfEcho() bool { return echoOff }
