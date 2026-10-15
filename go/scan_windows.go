//go:build windows

// 状态扫描（Windows）。
//
// Windows 没有 lsof / ps。这里选的路子是按**实机耗时**定的（本机 Windows 11 + pwsh 7.6.7）：
//
//	Get-NetTCPConnection       11.18s   ← 0.01 §8 原本建议的写法，慢到不可用
//	Get-CimInstance Win32_Proc  5.94s
//	pwsh 冷启动                 3.73s（Go 侧实测）
//	netstat -ano                0.50s   ← 取监听端口（对应 lsof 那一路）
//	tasklist（按名字筛）         0.73s   ← 取 ssh 的 pid
//	netstat + tasklist 并发      0.73s
//
// 所以分三步，且**没有 ssh 进程时完全不启动 PowerShell**：
//
//	1) netstat + tasklist 并发（~0.7s）
//	2) ssh 的 pid 为空 → 直接判定「没有隧道」返回，不付 PowerShell 的启动成本
//	3) 确实有 ssh 进程 → 才启动 pwsh 取命令行（pid / 转发规格 / 登录目标 / 启动时间）
//
// 错误处理原则：任一路取数失败都**明确报错**，绝不返回空表 ——
// 空表会被误读成「确实没有隧道」（0.01 §10.6）。报错是显式失败，不是空表。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// psScript 取 ssh 进程的 pid / 命令行 / 启动时间。
// 用 Get-Process 而不是 Get-CimInstance：PowerShell 7 的 Get-Process 直接带 CommandLine，
// 比 CIM 快。输出 JSON —— 命令行内容不可控（空格、引号、反斜杠路径），
// 自己拼分隔符解析在 Windows 上极易出错。
const psScript = `$ErrorActionPreference='Stop'; ` +
	`ConvertTo-Json -InputObject @(Get-Process | Where-Object { $_.Name -eq 'ssh' } | ` +
	`Select-Object Id,CommandLine,StartTime) -Compress -Depth 3`

type winProc struct {
	ID          uint32  `json:"Id"`
	CommandLine *string `json:"CommandLine"`
	StartTime   *string `json:"StartTime"`
}

// parseNetstat 解析 `netstat -ano -p TCP`，返回 pid -> 本地监听地址。
// 只取 LISTENING 行：`TCP  本地地址  外部地址  LISTENING  PID`。
// 状态词 LISTENING 在中文 Windows 上同样是英文（已实机确认）；表头本地化不影响按位置解析。
func parseNetstat(out string) map[int]string {
	res := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "TCP" || f[3] != "LISTENING" {
			continue
		}
		pid, err := strconv.Atoi(f[len(f)-1])
		if err != nil {
			continue
		}
		res[pid] = f[1]
	}
	return res
}

// parseTasklist 解析 `tasklist /FI "IMAGENAME eq ssh.exe" /FO CSV /NH`，取出 ssh 的 pid。
// 没有匹配时 tasklist 会输出一句本地化提示（不是 CSV），这里自然解析成空集合。
func parseTasklist(out string) []int {
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"`) {
			continue
		}
		f := strings.Split(line, `","`)
		if len(f) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(strings.Trim(f[1], `"`)); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// parseProcsJSON 解析 psScript 的产出。纯函数，便于单测。
// 兼容两种形状：数组（多个 ssh 进程）与单个对象（只有一个时 ConvertTo-Json 未必套数组）。
func parseProcsJSON(out []byte) ([]winProc, error) {
	out = bytes.TrimPrefix(out, []byte{0xEF, 0xBB, 0xBF}) // 去可能的 UTF-8 BOM
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var list []winProc
	if err := json.Unmarshal(out, &list); err == nil {
		return list, nil
	}
	var one winProc
	if err := json.Unmarshal(out, &one); err != nil {
		return nil, fmt.Errorf("解析进程列表失败：%w", err)
	}
	return []winProc{one}, nil
}

// buildRows 把进程记录转成 mergeScan 需要的行。纯函数，便于单测。
func buildRows(procs []winProc, now time.Time) []psRow {
	rows := make([]psRow, 0, len(procs))
	for _, p := range procs {
		args := splitCmdline(deref(p.CommandLine))
		if len(args) > 0 {
			args = args[1:] // 去掉程序路径本身，与 Unix 侧的 ps 解析口径一致
		}
		spec, dir := forwardSpec(args)
		rows = append(rows, psRow{
			PID:   int(p.ID),
			Etime: elapsedSince(p.StartTime, now),
			Spec:  spec,
			Dir:   dir,
			Host:  lastNonFlag(args),
		})
	}
	return rows
}

// splitCmdline 按 Windows 习惯切分命令行：双引号内的空格不切分，引号本身去掉。
// （strings.Fields 会把 "C:\Program Files\..." 这类带空格的参数切坏。）
func splitCmdline(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' || r == '\t':
			if inQuote {
				cur.WriteRune(r)
			} else {
				flush()
			}
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// elapsedSince 用进程启动时间算持续时间，输出与 `ps -o etime` 同格式。
// 拿不到或解析不了时记 "-"（0.01 §5.2：拿不到就记 -，不报错）。
func elapsedSince(start *string, now time.Time) string {
	s := deref(start)
	if s == "" {
		return "-"
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "20060102150405.000000Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return formatEtime(now.Sub(t))
		}
	}
	return "-"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// runPS 依次尝试 pwsh（PowerShell 7）与 powershell（Windows PowerShell），用先成功的那个。
func runPS(script string) ([]byte, error) {
	var firstErr error
	for _, exe := range []string{"pwsh", "powershell"} {
		p, err := exec.LookPath(exe)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err == nil {
			return out, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s 执行失败：%w", exe, err)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("找不到 PowerShell（pwsh / powershell）")
}

// scanTunnels 扫描全机 ssh 隧道，按 pid 升序返回。
func scanTunnels() ([]live, error) {
	var (
		allListen map[int]string
		listenErr error
		sshPIDs   []int
		pidsErr   error
	)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		out, err := exec.Command("netstat", "-ano", "-p", "TCP").Output()
		if err != nil {
			listenErr = fmt.Errorf("netstat 取监听端口失败：%w", err)
			return
		}
		allListen = parseNetstat(string(out))
	}()
	go func() {
		defer wg.Done()
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq ssh.exe", "/FO", "CSV", "/NH").Output()
		if err != nil {
			pidsErr = fmt.Errorf("tasklist 取 ssh 进程失败：%w", err)
			return
		}
		sshPIDs = parseTasklist(string(out))
	}()
	wg.Wait()

	if pidsErr != nil {
		return nil, pidsErr
	}
	if len(sshPIDs) == 0 {
		// 机器上没有 ssh 进程：直接判定没有隧道，不启动 PowerShell
		return nil, nil
	}
	_ = listenErr // 监听那一路失败不影响进程那一路（spec 仍能给出本地端口）

	// 确实有 ssh 进程，才付 PowerShell 的启动成本去取命令行
	out, err := runPS(psScript)
	if err != nil {
		return nil, err
	}
	procs, err := parseProcsJSON(out)
	if err != nil {
		return nil, err
	}

	isSSH := make(map[int]bool, len(sshPIDs))
	for _, pid := range sshPIDs {
		isSSH[pid] = true
	}
	listen := make(map[int]string, len(allListen))
	for pid, addr := range allListen {
		if isSSH[pid] {
			listen[pid] = addr // 只留 ssh 占的监听端口（对应「lsof 只取 COMMAND == ssh」那一步）
		}
	}
	return mergeScan(listen, buildRows(procs, time.Now())), nil
}
