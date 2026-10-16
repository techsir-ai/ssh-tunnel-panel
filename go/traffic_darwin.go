//go:build darwin

// 通讯量取数（macOS）：本机自带 nettop，是唯一提供进程级收发字节的系统工具。
package main

import "os/exec"

// trafficMap 返回 pid → 人读的通讯量（`4.0 MiB`）。
//
// 拿不到（没有 nettop、命令失败、输出解析不出来）就返回 nil ——
// 调用方据此把这一列记 `-`：**不报错、不留空**（0.01 §5.2 规则 2）。
func trafficMap() map[int]string {
	out, err := exec.Command("nettop", "-P", "-x", "-n", "-L", "1", "-p", "ssh").Output()
	if err != nil {
		return nil
	}
	raw := parseNettop(out)
	if len(raw) == 0 {
		return nil
	}
	res := make(map[int]string, len(raw))
	for pid, b := range raw {
		res[pid] = humanBytes(b)
	}
	return res
}
