//go:build !windows

// 状态扫描（macOS / Linux）：两道数据源，各自独立产生记录。
//
//	lsof —— 权威的本地监听地址，不会漏 ~/.ssh/config 里的 LocalForward
//	ps   —— 命令行里的 -L / -R 规格与登录目标，lsof 看不到转发意图
//
// 两路按 pid 合并（mergeScan，见 scan.go）。任一路拿不到数据，
// 另一路仍然照常输出 —— 绝不出现「整张表静默变空」（0.01 §10.6）。
package main

import (
	"os/exec"
	"path"
	"strconv"
	"strings"
)

// parseLsof 解析 `lsof -nP -iTCP -sTCP:LISTEN` 的输出，返回 pid -> 本地监听地址。
// 只取 COMMAND 为 ssh 的行；首行是表头跳过。
func parseLsof(out string) map[int]string {
	res := map[int]string{}
	for i, line := range strings.Split(out, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		// COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
		if len(f) < 9 || path.Base(f[0]) != "ssh" {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		res[pid] = f[8]
	}
	return res
}

// parsePs 解析 `ps -axww -o pid=,etime=,command=` 的输出，挑出 ssh 进程。
func parsePs(out string) []psRow {
	var rows []psRow
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// f[0]=pid f[1]=etime f[2]=程序 f[3:]=参数
		if len(f) < 3 || path.Base(f[2]) != "ssh" {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		spec, dir := forwardSpec(f[3:])
		rows = append(rows, psRow{PID: pid, Etime: f[1], Spec: spec, Dir: dir, Host: lastNonFlag(f[3:])})
	}
	return rows
}

// scanTunnels 扫描全机 ssh 隧道，按 pid 升序返回。
// 只有两路都没取到任何输出时才返回 error —— 调用方据此明确报错，而不是当作「没有隧道」。
func scanTunnels() ([]live, error) {
	lsofOut, _ := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
	psOut, _ := exec.Command("ps", "-axww", "-o", "pid=,etime=,command=").Output()
	if len(lsofOut) == 0 && len(psOut) == 0 {
		return nil, errScanUnavailable
	}
	return mergeScan(parseLsof(string(lsofOut)), parsePs(string(psOut))), nil
}
