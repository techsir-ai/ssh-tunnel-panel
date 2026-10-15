//go:build windows

// 终端控制（Windows）—— TUI 前端的最小依赖。
//
// Windows 控制台**默认是行缓冲的**（ENABLE_LINE_INPUT）：不关掉它，
// 「按任意键」实际要按回车才会被程序读到，3/5 秒的按键窗口就形同虚设。
// 所以这里直接调 kernel32 的 GetConsoleMode / SetConsoleMode（stdlib syscall 即可，无外部依赖）。
//
// 另外 ANSI 转义序列（清屏）在 Windows 控制台上需要显式打开
// ENABLE_VIRTUAL_TERMINAL_PROCESSING，否则会打出一堆乱码字符。
package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

const (
	enableProcessedInput  = 0x0001
	enableLineInput       = 0x0002
	enableEchoInput       = 0x0004
	enableVirtualTerminal = 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING（输出模式位）
)

func getConsoleMode(h uintptr) (uint32, bool) {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	return mode, r != 0
}

func setConsoleMode(h uintptr, mode uint32) bool {
	r, _, _ := procSetConsoleMode.Call(h, uintptr(mode))
	return r != 0
}

// isTerminal 判断 stdin 是不是控制台（管道 / 重定向时为 false）。
func isTerminal() bool {
	_, ok := getConsoleMode(uintptr(syscall.Stdin))
	return ok
}

// clearScreen 清屏 + 光标归位（需输出句柄已开 VT）。
func clearScreen() { os.Stdout.WriteString("\033[2J\033[H") }

var (
	savedInMode uint32
	savedInOK   bool
	savedOutVT  bool
)

// termInit 关行缓冲与回显（单键即时可读），并给输出打开 ANSI 支持。
func termInit() {
	if m, ok := getConsoleMode(uintptr(syscall.Stdin)); ok {
		savedInMode, savedInOK = m, true
		setConsoleMode(uintptr(syscall.Stdin), (m&^(enableLineInput|enableEchoInput))|enableProcessedInput)
	}
	if m, ok := getConsoleMode(uintptr(syscall.Stdout)); ok && m&enableVirtualTerminal == 0 {
		if setConsoleMode(uintptr(syscall.Stdout), m|enableVirtualTerminal) {
			savedOutVT = true
		}
	}
}

// termRestore 恢复控制台的原始模式（退出 TUI 前必须调）。
func termRestore() {
	if savedInOK {
		setConsoleMode(uintptr(syscall.Stdin), savedInMode)
	}
	if savedOutVT {
		if m, ok := getConsoleMode(uintptr(syscall.Stdout)); ok {
			setConsoleMode(uintptr(syscall.Stdout), m&^enableVirtualTerminal)
		}
		savedOutVT = false
	}
}

// termCooked 恢复行输入（含回显）—— 逐行提问时用。
func termCooked() {
	if savedInOK {
		setConsoleMode(uintptr(syscall.Stdin), savedInMode)
	}
}

// termRaw 回到单键模式。
func termRaw() {
	if savedInOK {
		setConsoleMode(uintptr(syscall.Stdin), (savedInMode&^(enableLineInput|enableEchoInput))|enableProcessedInput)
	}
}
