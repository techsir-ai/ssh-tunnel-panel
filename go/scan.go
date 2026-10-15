// 状态扫描的共享部分：结果模型、排序、转发规格解析。
// 平台相关的取数分别见 scan_unix.go / scan_windows.go。
package main

import (
	"errors"
	"strings"
)

// errScanUnavailable 表示两道数据源都没能取到数据。
// 此时必须由调用方**明确报错**，绝不能返回空表当作「没有隧道」（0.01 §10.6）。
var errScanUnavailable = errors.New("扫描不可用：数据源未能取到数据")

// live 是一条正在运行的隧道（扫描结果）。
type live struct {
	PID   int    // 进程号
	Dir   string // 方向：L / R（0.02 §2.1）
	Local string // 本地监听 ip:端口 —— 永远指本机（权威来源：lsof）
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

// splitSpec 把命令行上的转发规格拆成「本地 / 远程」两段，列义固定为
// 第 3 列永远指本机、第 4 列永远指远端（0.02 §5）。
//
// 段数规则与 bash 版一致：
//
//	4 段 -> a1:a2 为前段、a3:a4 为后段
//	3 段 -> 前段补 127.0.0.1、即 127.0.0.1:a1 与 a2:a3
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
