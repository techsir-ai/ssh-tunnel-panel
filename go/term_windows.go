//go:build windows

// 终端控制（Windows）—— TUI 前端的最小依赖。
//
// Windows 控制台**默认是行缓冲的**（ENABLE_LINE_INPUT + ENABLE_ECHO_INPUT）：
// 不关掉它，「按任意键」实际要按回车，键入也不会有即时反馈。
// 所以这里直接调 kernel32 的 GetConsoleMode / SetConsoleMode（stdlib syscall 即可，无外部依赖）。
//
// 另外 ANSI 转义序列（清屏）在 Windows 控制台上需要显式打开
// ENABLE_VIRTUAL_TERMINAL_PROCESSING（输出模式位），否则会打出一串乱码。
//
// 注意两个同值不同域的常量：输入域的 ENABLE_ECHO_INPUT 与输出域的
// ENABLE_VIRTUAL_TERMINAL_PROCESSING 都是 0x0004，别混用。
//
// 句柄一律用 os.Stdin.Fd() / os.Stdout.Fd()，而不是硬编码 0/1/2：
// stdin 可能被改成管道或别的句柄，模式要设在真正在读的那个句柄上。
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
	enableProcessedInput  = 0x0001 // 输入域：Ctrl+C 交给系统处理
	enableLineInput       = 0x0002 // 输入域：行缓冲（必须关掉）
	enableEchoInput       = 0x0004 // 输入域：终端回显（关掉后由本程序回显）
	enableVirtualTerminal = 0x0004 // 输出域：ANSI 转义序列
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
	_, ok := getConsoleMode(os.Stdin.Fd())
	return ok
}

// clearScreen 清屏 + 光标归位（需输出句柄已开 VT）。
func clearScreen() { _, _ = os.Stdout.WriteString("\033[2J\033[H") }

var (
	savedInMode uint32
	savedInOK   bool
	savedOutVT  bool
	rawOK       bool // 行缓冲确实关掉了（否则按键仍需回车）
	echoOff     bool // 终端确实不回显了（此时必须由本程序回显）
)

// termInit 进入单键模式：关行缓冲、关回显；并给输出打开 ANSI 支持。
func termInit() {
	if m, ok := getConsoleMode(os.Stdin.Fd()); ok {
		savedInMode, savedInOK = m, true
		want := (m &^ (enableLineInput | enableEchoInput)) | enableProcessedInput
		if setConsoleMode(os.Stdin.Fd(), want) {
			if cur, ok2 := getConsoleMode(os.Stdin.Fd()); ok2 {
				rawOK = cur&enableLineInput == 0
				echoOff = cur&enableEchoInput == 0
			}
		}
	}
	ho := os.Stdout.Fd()
	if m, ok := getConsoleMode(ho); ok && m&enableVirtualTerminal == 0 {
		if setConsoleMode(ho, m|enableVirtualTerminal) {
			savedOutVT = true
		}
	}
}

// termRestore 恢复控制台的原始模式（退出 TUI 前必须调，否则终端会一直"哑"着）。
func termRestore() {
	if savedInOK {
		setConsoleMode(os.Stdin.Fd(), savedInMode)
	}
	if savedOutVT {
		if m, ok := getConsoleMode(os.Stdout.Fd()); ok {
			setConsoleMode(os.Stdout.Fd(), m&^enableVirtualTerminal)
		}
		savedOutVT = false
	}
}

// singleKeyOK 单键模式是否真的生效。未生效（终端不给改）时按键需要回车，
// 这种情况要如实提示用户，而不是让人对着屏幕猜。
func singleKeyOK() bool { return rawOK }

// needSelfEcho 是否需要本程序自己回显（终端已关回显时必须）。
func needSelfEcho() bool { return echoOff }
