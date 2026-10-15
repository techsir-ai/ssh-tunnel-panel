// 状态扫描的共享部分：结果模型、排序、转发规格解析、两路合并、时长格式化。
// 平台相关的取数分别见 scan_unix.go（lsof + ps）与 scan_windows.go（CIM + NetTCPConnection）。
package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// errScanUnavailable 表示数据源都没能取到数据。
// 此时必须由调用方**明确报错**，绝不能返回空表当作「没有隧道」（0.01 §10.6）。
var errScanUnavailable = errors.New("扫描不可用：数据源未能取到数据")

// live 是一条正在运行的隧道（扫描结果）。
type live struct {
	PID   int    // 进程号
	Dir   string // 方向：L / R（0.02 §2.1）
	Local string // 本地监听 ip:端口 —— 永远指本机（权威来源：系统查询）
	Rem   string // 转发目标 ip:端口 —— 永远指远端
	Host  string // SSH 主机（登录目标）
	Etime string // 持续时间，如 03:07:41 / 2-11:00:09；拿不到记 "-"
}

// sortByPID 按 pid 升序 —— 保证编号在状态区与断开区一致（0.01 §10.7）。
func sortByPID(ls []live) {
	for i := 1; i < len(ls); i++ {
		for j := i; j > 0 && ls[j].PID < ls[j-1].PID; j-- {
			ls[j], ls[j-1] = ls[j-1], ls[j]
		}
	}
}

// psRow 是一条 ssh 进程记录。两个平台各自从自己的数据源产出这个结构，再交给 mergeScan。
type psRow struct {
	PID   int
	Etime string
	Spec  string // 转发规格，如 3081:127.0.0.1:3080
	Dir   string // L / R
	Host  string // 登录目标
}

// splitSpec 把命令行上的转发规格拆成「本地 / 远程」两段，列义固定为
// 第 3 列永远指本机、第 4 列永远指远端（0.02 §5）。
//
// 段数规则与 bash 版一致：
//
//	4 段 -> a1:a2 为前段、a3:a4 为后段
//	3 段 -> 前段补 127.0.0.1，即 127.0.0.1:a1 与 a2:a3
//	其余 -> 解析不出来
//
// 方向为 R 时，命令行上的顺序与配置列相反（0.02 §5：R → ssh -N -R <列4>:<列3>），
// 所以解析后要把两段换回来，否则「本地」会指到远端去。
func splitSpec(spec, dir string) (loc, rem string, ok bool) {
	p := strings.Split(spec, ":")
	var a, b string
	switch len(p) {
	case 4:
		a, b = p[0]+":"+p[1], p[2]+":"+p[3]
	case 3:
		a, b = "127.0.0.1:"+p[0], p[1]+":"+p[2]
	default:
		return "", "", false
	}
	if dir == "R" {
		return b, a, true
	}
	return a, b, true
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
// 传进来的 args 应当已经去掉「进程号 / 时长 / 程序路径」这些前缀。
func lastNonFlag(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
	}
	return ""
}

// mergeScan 把「本地监听」与「进程记录」两路数据按 pid 合并成状态表需要的行。
//
// 关键点（0.01 §10.6）：两路各自都能独立产生记录。某一侧整体拿不到数据时，
// 另一侧照常输出，绝不因一路失败而整表变空。
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
			loc = listen[pid] // 监听端口以系统查询为准
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

// formatEtime 把时长格式化成 `ps -o etime` 的样子：03:07:41 / 2-11:00:09。
// Windows 没有 ps，用进程创建时间算出来，格式与 macOS 保持一致。
func formatEtime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	days := total / 86400
	h := (total % 86400) / 3600
	m := (total % 3600) / 60
	s := total % 60
	if days > 0 {
		return fmt.Sprintf("%d-%02d:%02d:%02d", days, h, m, s)
	}
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
