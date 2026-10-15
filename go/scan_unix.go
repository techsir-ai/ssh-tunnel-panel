//go:build !windows

// 状态扫描（macOS / Linux）：两道数据源，各自独立产生记录。
//
//	lsof —— 权威的本地监听地址，不会漏 ~/.ssh/config 里的 LocalForward
//	ps   —— 命令行里的 -L / -R 规格与登录目标，lsof 看不到转发意图
//
// 两路按 pid 合并。任一路拿不到数据（lsof 受限、ps 受限、或隧道来自 ssh_config），
// 另一路仍然照常输出 —— 绝不出现「整张表静默变空」（0.01 §10.6）。
package main

import (
	"os/exec"
	"path"
	"strconv"
	"strings"
)

// psRow 是 ps 输出里挑出的一条 ssh 进程记录。
type psRow struct {
	PID   int
	Etime string
	Spec  string // 转发规格，如 3081:127.0.0.1:3080
	Dir   string // L / R
	Host  string // 登录目标
}

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
		rows = append(rows, psRow{PID: pid, Etime: f[1], Spec: spec, Dir: dir, Host: lastNonFlag(f)})
	}
	return rows
}

// forwardSpec 在参数表里找转发规格，返回 (规格, 方向)。
// 先找 -L / -R（含贴写的 -L3081:… 形式），再找 -o LocalForward= / RemoteForward=。
func forwardSpec(args []string) (spec, dir string) {
	for i, a := range args {
		switch {
		case a == "-L" || a == "-R":
			if i+1 < len(args) {
				return args[i+1], string(a[1])
			}
		case strings.HasPrefix(a, "-L") && len(a) > 2:
			return a[2:], "L"
		case strings.HasPrefix(a, "-R") && len(a) > 2:
			return a[2:], "R"
		}
	}
	const lf, rf = "LocalForward=", "RemoteForward="
	for i, a := range args {
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		if a == "-o" || a == "-O" {
			if strings.HasPrefix(next, lf) {
				return next[len(lf):], "L"
			}
			if strings.HasPrefix(next, rf) {
				return next[len(rf):], "R"
			}
		}
		if strings.HasPrefix(a, lf) {
			return a[len(lf):], "L"
		}
		if strings.HasPrefix(a, rf) {
			return a[len(rf):], "R"
		}
	}
	return "", ""
}

// lastNonFlag 取登录目标：从末尾往前找第一个不以 - 开头的参数（对齐 bash 版）。
func lastNonFlag(f []string) string {
	for i := len(f) - 1; i >= 2; i-- {
		if !strings.HasPrefix(f[i], "-") {
			return f[i]
		}
	}
	return ""
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

// mergeScan 把两路数据按 pid 合并成状态表需要的行。
func mergeScan(listen map[int]string, rows []psRow) []live {
	type acc struct {
		pid    int
		spec   string
		dir    string
		host   string
		etime  string
		hasLsf bool
	}
	byPID := map[int]*acc{}
	touch := func(pid int) *acc {
		a, ok := byPID[pid]
		if !ok {
			a = &acc{pid: pid, dir: "L"}
			byPID[pid] = a
		}
		return a
	}
	for pid := range listen {
		touch(pid).hasLsf = true
	}
	for _, r := range rows {
		a := touch(r.PID)
		a.etime, a.host = r.Etime, r.Host
		if r.Spec != "" {
			a.spec, a.dir = r.Spec, r.Dir
		}
	}

	res := make([]live, 0, len(byPID))
	for pid, a := range byPID {
		if a.spec == "" && !a.hasLsf {
			continue // 没有转发，不是隧道
		}
		loc, rem, ok := splitSpec(a.spec, a.dir)
		if !ok {
			loc, rem = "-", a.spec
			if a.spec == "" {
				rem = "-"
			}
		}
		if a.hasLsf {
			loc = listen[pid] // lsof 权威
		}
		host, etime := a.host, a.etime
		if host == "" {
			host = "-"
		}
		if etime == "" {
			etime = "-"
		}
		res = append(res, live{PID: pid, Dir: a.dir, Local: loc, Rem: rem, Host: host, Etime: etime})
	}
	sortByPID(res)
	return res
}
