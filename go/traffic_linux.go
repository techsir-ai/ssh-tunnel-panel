//go:build linux

// 通讯量取数（Linux）：用 `ss -tinp`。
//
// 为什么这样取：内核的 TCP_INFO 本来就按**每条连接**给累计字节
//（bytes_sent / bytes_acked / bytes_received），`ss -i` 只是把它打出来；
// `-p` 又把 socket 归属到进程。两者一拼，就是「这个 ssh 进程的隧道走了多少字节」。
// 不需要 root（看自己起的进程足够），也不需要 nethogs / eBPF 之类新依赖。
//
// 与 macOS 的口径折算：nettop 给的是**进程的全部网络字节**（本地一侧 + 远端一侧），
// 所以 bash 版要 ÷2 才是真正穿过隧道的数据量；这里只统计 ssh 与远端那**一条**连接，
// 两个方向各算一次，本来就是穿过隧道的量 —— **不需要再 ÷2**。
//
// 拿不到（没有 ss / 命令失败 / 解析不出）就返回 nil → 整列记 `-`。
package main

import "os/exec"

func trafficMap() map[int]string {
	out, err := exec.Command("ss", "-tinp").Output()
	if err != nil {
		return nil
	}
	raw := parseSS(out)
	if len(raw) == 0 {
		return nil
	}
	res := make(map[int]string, len(raw))
	for pid, b := range raw {
		res[pid] = humanBytes(b)
	}
	return res
}
