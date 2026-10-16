//go:build windows

// 起停的平台部分（Windows）。
package main

import (
	"os"
	"syscall"
)

const (
	detachedProcess       = 0x00000008 // 不给新进程分配控制台（不占 console，0.01 §4.2）
	createNewProcessGroup = 0x00000200 // 不被本程序的 Ctrl+C 一起带走
	stillActive           = 259        // STILL_ACTIVE
	processQueryLimited   = 0x1000     // PROCESS_QUERY_LIMITED_INFORMATION
)

// detachedAttrs 让 ssh 脱离本程序的控制台。
//
// 为什么不沿用 unix 的「继承终端」做法：Windows 上一个进程要么有控制台要么没有，
// 让 ssh 继续挂在同一个控制台上，它就会去抢键盘（口令提示），把 TUI 搅乱。
// DETACHED_PROCESS 相当于让它自己去后台待着。
func detachedAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}

// killPID 结束进程（对齐 bash 版 `kill` 的语义：结束掉就算成功）。
func killPID(pid int) error {
	p, err := os.FindProcess(pid) // Windows 上不做校验，总能拿到
	if err != nil {
		return err
	}
	return p.Kill()
}

// processAlive 进程是否还在。
// 取不到句柄 = 进程已不存在（或没权限查询），按"不在"处理。
func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
