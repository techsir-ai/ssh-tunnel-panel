//go:build !windows

// 起停的平台部分（macOS / Linux）。
package main

import (
	"errors"
	"syscall"
)

// detachedAttrs 让 ssh 脱离本程序：
// Setsid 开新会话、没有控制终端 —— 这就是 0.01 §10.3 里 `nohup ... &` 的等价物，
// 本程序退出后隧道继续活，终端信号也带不倒它。
func detachedAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// killPID 发 TERM（对齐 bash 版的 `kill`）。
// 进程已经不在 → 视为正常（调用方按「成功」记账，与 bash 的 kill_one 一致）。
func killPID(pid int) error {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// processAlive 进程是否还在（`kill -0`）。
func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
