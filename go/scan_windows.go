//go:build windows

// 状态扫描（Windows）—— 尚未实现。
//
// TODO(0.01 §8)：Windows 无 lsof，等价实现是
// Get-CimInstance Win32_Process（全系统进程 + CommandLine）+ Get-NetTCPConnection（监听端口）。
//
// 在实现之前**必须明确报错，绝不返回空表**：
// 空表会被调用方（尤其是 agent）误读成「确实没有隧道」，然后基于假事实往下走 ——
// 这比报错危险得多（0.01 §10.6）。
package main

import "errors"

func scanTunnels() ([]live, error) {
	return nil, errors.New("Windows 扫描尚未实现（Go 版开发中）")
}
