// 通讯量列（0.01 §5.2 第 6 列 / §8 平台边界 / §10.14 取数口径）。
//
// 取数：`nettop -P -x -n -L 1 -p ssh` 的 CSV；第 2 列是 `name.pid`，
// 第 5 / 6 列是 bytes_in / bytes_out。
//
// 口径：**收发合计 ÷ 2** —— bytes_in + bytes_out 把同一份数据算了两遍
//（本机应用收进一次、ssh 再发往远端一次），所以除以 2。
//
// 平台边界（0.01 §8 明写）：**这是 macOS 专属能力**。Linux 没有进程级网络字节计数
//（要 nethogs / eBPF，等于新增依赖），Windows 也只有 IO 计数器近似 ——
// 这两平台这一列记 `-`，**不是漏实现**。
//
// 另：只显示**累计量**，不做速率（0.01 §8 明确排除「通讯量按速率显示」）。
package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parseNettop 解析 nettop 的 CSV：pid → 收发合计 ÷ 2 的字节数。
//
// 纯函数（便于单测）。表头行与字段不齐、字段非数字的行一律跳过 ——
// nettop 的表头里 bytes_in 位置是字面量 "bytes_in"，用数字判断即可自然滤掉。
func parseNettop(out []byte) map[int]int64 {
	res := map[int]int64{}
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	for i, line := range strings.Split(text, "\n") {
		if i == 0 { // 表头
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 6 {
			continue
		}
		// 第 2 列形如 ssh.91215 —— 取最后一个点之后的部分当 pid
		name := strings.TrimSpace(f[1])
		dot := strings.LastIndex(name, ".")
		if dot < 0 || dot == len(name)-1 {
			continue
		}
		pid, err := strconv.Atoi(name[dot+1:])
		if err != nil || pid <= 0 {
			continue
		}
		in, err1 := strconv.ParseInt(strings.TrimSpace(f[4]), 10, 64)
		outB, err2 := strconv.ParseInt(strings.TrimSpace(f[5]), 10, 64)
		if err1 != nil || err2 != nil || in < 0 || outB < 0 {
			continue
		}
		res[pid] = (in + outB) / 2
	}
	return res
}

// parseSS 解析 `ss -tinp`（Linux）的输出：pid → 收发合计字节。
//
// ss 的形态（`-i` 给扩展信息、`-p` 给进程）：
//
//	ESTAB 0 0 192.168.8.100:22 192.168.8.10:7069 users:(("sshd-session",pid=1630310,fd=7))
//		 cubic ... bytes_sent:4325 bytes_acked:4325 bytes_received:4006 segs_out:21 ...
//
// 进程信息在**主行**，字节计数在紧跟的**缩进行**；一个 socket 可能列出多个 pid（共享 fd），
// 每个 pid 各记一份（`ssh -N` 的隧道一条连接一个 pid，不存在重复计数）。
//
// 发送侧取 `bytes_sent` 与 `bytes_acked` 的较大者：新内核已把 `bytes_sent` 标为废弃，
// 有的版本会读成 0，这时用 `bytes_acked` 兜底。
//
// 纯函数，便于单测。
func parseSS(out []byte) map[int]int64 {
	res := map[int]int64{}
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	var curPids []int
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' { // 扩展信息行：把字节记到主行解析出的 pid 上
			sent, _ := ssField(line, "bytes_sent")
			acked, _ := ssField(line, "bytes_acked")
			recv, okR := ssField(line, "bytes_received")
			if !okR {
				continue
			}
			if acked > sent {
				sent = acked
			}
			for _, pid := range curPids {
				res[pid] += sent + recv
			}
			continue
		}
		curPids = ssPids(line)
	}
	return res
}

// ssPids 从 ss 的一行里取出所有 pid（users:(("name",pid=123,fd=4),...)）。
func ssPids(line string) []int {
	var res []int
	rest := line
	for {
		i := strings.Index(rest, "pid=")
		if i < 0 {
			return res
		}
		rest = rest[i+len("pid="):]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 {
			continue
		}
		if n, err := strconv.Atoi(rest[:j]); err == nil && n > 0 {
			res = append(res, n)
		}
		rest = rest[j:]
	}
}

// ssField 从一行里取 `key:数字`。
func ssField(line, key string) (int64, bool) {
	pat := key + ":"
	i := strings.Index(line, pat)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(pat):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(rest[:j], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// humanBytes 字节数 → 人读单位（0.01：`79.9 KiB` 形式）。
// 不足 1 KiB 时是整数 + `B`（`512 B`），与 bash 版的 human_bytes 一致。
func humanBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
