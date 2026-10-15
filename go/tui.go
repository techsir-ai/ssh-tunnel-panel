// TUI 前端 —— 交互（清屏 / 读键 / 暂停）。
//
// 与 0.01 §5.3 / §5.4 对齐：
//   - 整屏重绘（清屏 + 重印），不移动光标、不做局部刷新
//   - 不引入 curses / whiptail / dialog 等任何外部库
//   - 不按键则每 tickSec 秒自动刷新一次（状态表含"持续时间/通讯量"，是活的量）
//   - 退出（q）= 只退出程序，不改变任何隧道状态
//
// 键位与 CLI 完全一致：同一个键，TUI 里是「交互 → 执行」，CLI 里是「一次性执行 → 退出」，
// 差的只是这个 while 循环。
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 读键 ----------

var (
	keyCh     = make(chan byte, 8)
	keyReader sync.Once
)

// startKeyReader 起**唯一**一个读 stdin 的协程，只起一次。
// 不能每次读键都开一个协程：那样每个超时周期都会留下一个永久阻塞在 Read 上的协程。
func startKeyReader() {
	keyReader.Do(func() {
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil || n == 0 {
					close(keyCh) // stdin 关了（终端消失 / 管道结束）
					return
				}
				keyCh <- buf[0]
			}
		}()
	})
}

// readKey 读一个字符（不回显、不必回车 —— 依赖 termInit 关掉行缓冲）。
//   - 超时没键：ok=false
//   - stdin 已关：closed=true，调用方据此退出，避免空转（0.01 §10.12）
func readKey(timeout time.Duration) (b byte, ok bool, closed bool) {
	startKeyReader()
	select {
	case v, more := <-keyCh:
		if !more {
			return 0, false, true
		}
		return v, true, false
	case <-time.After(timeout):
		return 0, false, false
	}
}

// pauseKey 等一个键再返回。子流程结束、或出错需要让人看清时用。
func pauseKey() {
	fmt.Print("\n按任意键返回...")
	startKeyReader()
	<-keyCh
}

// readLine 读一行（逐行提问用）。读取期间临时恢复行输入模式，
// 让终端自己处理回显与退格；读完马上回到单键模式。
func readLine() string {
	startKeyReader()
	termCooked()
	defer termRaw()
	var out []byte
	for v := range keyCh {
		if v == '\n' {
			break
		}
		if v == '\r' {
			continue
		}
		out = append(out, v)
	}
	return string(out)
}

func clr() { clearScreen() }

// ---------- 菜单 ----------

// mainMenu 主循环。preset 非 0 表示"无参数入口那个按键直接生效"（0.01 §5.1）。
func mainMenu(preset byte) {
	first := preset != 0
	for {
		clr()
		showStatus()
		printKeys()
		fmt.Print("> ")

		var key byte
		if first {
			key = preset
			first = false
			fmt.Print("\n")
		} else {
			k, ok, closed := readKey(tickSec * time.Second)
			fmt.Print("\n")
			if closed {
				return // 终端没了，退出，不要空转
			}
			if !ok {
				continue // 超时 = 没按键 → 重绘
			}
			key = k
		}

		switch key {
		case 'r', 'R':
			// 刷新 = 重绘（每轮循环本就整屏重绘）；把它显式写进键位表是给人一个明确的动作
		case 'v', 'V':
			doView()
		case 'n', 'N':
			doNew()
		case 'e', 'E':
			doEdit()
		case 'x', 'X':
			doDel()
		case 'c', 'C':
			doConnect()
		case 'd', 'D':
			doDisconnect()
		case 'q', 'Q':
			return
		}
	}
}

// ---------- 动作内的选择列表（不算二级菜单，0.01 §5.3） ----------

// printConfList 打印配置清单，返回可用的编号集合。
func printConfList() map[int]bool {
	avail := map[int]bool{}
	rows, _ := listConf()
	for _, r := range rows {
		t, ok := parseLine(r.Raw)
		if !ok {
			continue
		}
		avail[r.No] = true
		fmt.Printf("  %2d) [%s] %s -> %s   %s   (%s)\n", r.No, t.Dir, t.Local, t.Rem, t.Login, t.Desc)
	}
	return avail
}

// ---------- 六个动作 ----------

func doView() {
	clr()
	showConfig()
	pauseKey()
}

func doNew() {
	clr()
	fmt.Printf("新建（逐项输入；此行只写配置，不自动连接）\n\n")

	// 方向（0.02 §2.1：配置与界面都要能区分 L / R）
	fmt.Printf(" 1) 方向\n")
	fmt.Printf("    [1] L  本地转发（本机监听 → 转发到远端）  ← 默认\n")
	fmt.Printf("    [2] R  反向转发（远端监听 → 转发到本机）\n")
	fmt.Printf("    > ")
	dir := "L"
	switch strings.TrimSpace(readLine()) {
	case "", "1":
		dir = "L"
	case "2":
		dir = "R"
	default:
		fmt.Printf("\n请输入 1 或 2。\n")
		pauseKey()
		return
	}

	rows, _ := listConf()
	def := fmt.Sprintf("%02d", len(rows)+1)
	fmt.Printf("\n 2) 说明 —— 这条隧道的备注。任意文本、可含空格；不能含逗号（逗号是列分隔符）\n")
	fmt.Printf("    留空则自动使用序号：%s\n", def)
	fmt.Printf("    > ")
	desc := strings.TrimSpace(readLine())
	if desc == "" {
		desc = def
	}

	fmt.Printf("\n 3) 本地 IP —— 本机监听地址，如 127.0.0.1 或 192.168.10.100\n")
	fmt.Printf("    留空记 127.0.0.1\n")
	fmt.Printf("    > ")
	lip := strings.TrimSpace(readLine())
	if lip == "" {
		lip = "127.0.0.1"
	}

	fmt.Printf("\n 4) 本地端口 —— 本机监听端口，如 3081（必填）\n")
	fmt.Printf("    > ")
	lport := strings.TrimSpace(readLine())
	if !isPort(lport) {
		fmt.Printf("\n本地端口必须是 1-65535 的数字。\n")
		pauseKey()
		return
	}

	fmt.Printf("\n 5) 远程 IP —— 从远端视角解析，如 127.0.0.1 或 10.0.0.5\n")
	fmt.Printf("    留空记 127.0.0.1\n")
	fmt.Printf("    > ")
	rip := strings.TrimSpace(readLine())
	if rip == "" {
		rip = "127.0.0.1"
	}

	fmt.Printf("\n 6) 远程端口 —— 目标端口，如 3080（留空 = 取本地端口）\n")
	fmt.Printf("    > ")
	rport := strings.TrimSpace(readLine())
	if rport == "" {
		rport = lport // 0.02 §2.2
	}
	if !isPort(rport) {
		fmt.Printf("\n远程端口必须是 1-65535 的数字。\n")
		pauseKey()
		return
	}

	fmt.Printf("\n 7) 远程登录方式\n")
	fmt.Printf("    [1] user@host     显式指定用户名，如 changwei@x1c\n")
	fmt.Printf("    [2] config 名称    走 ~/.ssh/config，不写用户名，如 x1c\n")
	fmt.Printf("    > ")
	mode := strings.TrimSpace(readLine())

	var login string
	switch mode {
	case "1":
		fmt.Printf("\n 8) 用户名@主机（必填）\n    > ")
		login = strings.TrimSpace(readLine())
		if login == "" {
			fmt.Printf("\n不能为空。\n")
			pauseKey()
			return
		}
		if !strings.Contains(login, "@") {
			fmt.Printf("\n这种方式要写成 用户名@主机，如 changwei@x1c。\n")
			pauseKey()
			return
		}
		if strings.HasPrefix(login, "@") || strings.HasSuffix(login, "@") {
			fmt.Printf("\n用户名和主机都不能为空。\n")
			pauseKey()
			return
		}
	case "2":
		fmt.Printf("\n 8) config 名称 —— ~/.ssh/config 里的 Host 别名（必填）\n    > ")
		login = strings.TrimSpace(readLine())
		if login == "" {
			fmt.Printf("\nconfig 名称不能为空。\n")
			pauseKey()
			return
		}
		if strings.Contains(login, "@") {
			fmt.Printf("\n这种方式不应含 @；要指定用户名请回上一步选 [1]。\n")
			pauseKey()
			return
		}
	default:
		fmt.Printf("\n请输入 1 或 2。\n")
		pauseKey()
		return
	}

	t := Tunnel{Dir: dir, Desc: desc, Local: lip + ":" + lport, Rem: rip + ":" + rport, Login: login}
	for _, v := range []string{t.Dir, t.Desc, t.Local, t.Rem, t.Login} {
		if strings.Contains(v, ",") {
			fmt.Printf("\n任何一段都不能含逗号（逗号是列分隔符）。\n")
			pauseKey()
			return
		}
	}
	if err := appendConf(t); err != nil {
		fmt.Printf("\n写入失败：%v\n", err)
		pauseKey()
		return
	}
	fmt.Printf("\n已写入 %s\n", confPath())
	pauseKey()
}

func doEdit() {
	clr()
	fmt.Printf("修改配置（编号 = 配置文件行序号）\n\n")
	avail := printConfList()
	if len(avail) == 0 {
		fmt.Printf("  配置里还没有隧道，先按 n 新建。\n")
		pauseKey()
		return
	}

	fmt.Printf("\n输入要修改的编号（直接回车取消）: ")
	n, ok := atoiOK(readLine())
	if !ok {
		return
	}
	if !avail[n] {
		fmt.Printf("\n没有第 %d 条。\n", n)
		pauseKey()
		return
	}
	t, _ := confByNo(n)

	clr()
	fmt.Printf("第 %d 条当前内容：\n  %s\n\n", n, t.format())
	fmt.Printf(" 1) 方向        = %s\n", t.Dir)
	fmt.Printf(" 2) 说明        = %s\n", t.Desc)
	fmt.Printf(" 3) 本地 IP     = %s\n", ipOf(t.Local))
	fmt.Printf(" 4) 本地端口    = %s\n", portOf(t.Local))
	fmt.Printf(" 5) 远程 IP     = %s\n", ipOf(t.Rem))
	fmt.Printf(" 6) 远程端口    = %s\n", portOf(t.Rem))
	fmt.Printf(" 7) 远程登录    = %s\n", t.Login)

	fmt.Printf("\n输入要改的字段号 1-7（直接回车取消）: ")
	fno, ok := atoiOK(readLine())
	if !ok {
		return
	}

	nt := t
	switch fno {
	case 1:
		fmt.Printf("\n 新的方向 [1] L  [2] R（直接回车取消）: ")
		switch strings.TrimSpace(readLine()) {
		case "":
			return
		case "1":
			nt.Dir = "L"
		case "2":
			nt.Dir = "R"
		default:
			fmt.Printf("\n请输入 1 或 2。\n")
			pauseKey()
			return
		}
	case 2, 3, 4, 5, 6:
		name := map[int]string{2: "说明", 3: "本地 IP", 4: "本地端口", 5: "远程 IP", 6: "远程端口"}[fno]
		fmt.Printf("\n新的%s（直接回车取消）: ", name)
		val := strings.TrimSpace(readLine())
		if val == "" {
			return
		}
		if strings.Contains(val, ",") {
			fmt.Printf("\n新值不能含逗号（逗号是列分隔符）。\n")
			pauseKey()
			return
		}
		switch fno {
		case 2:
			nt.Desc = val
		case 3:
			nt.Local = val + ":" + portOf(t.Local)
		case 4:
			if !isPort(val) {
				fmt.Printf("\n端口必须是 1-65535 的数字。\n")
				pauseKey()
				return
			}
			nt.Local = ipOf(t.Local) + ":" + val
		case 5:
			nt.Rem = val + ":" + portOf(t.Rem)
		case 6:
			if !isPort(val) {
				fmt.Printf("\n端口必须是 1-65535 的数字。\n")
				pauseKey()
				return
			}
			nt.Rem = ipOf(t.Rem) + ":" + val
		}
	case 7:
		fmt.Printf("\n 新的远程登录方式\n")
		fmt.Printf("    [1] user@host   [2] config 名称\n    > ")
		switch strings.TrimSpace(readLine()) {
		case "1":
			fmt.Printf("    用户名@主机（直接回车取消）: ")
			val := strings.TrimSpace(readLine())
			if val == "" {
				return
			}
			if !strings.Contains(val, "@") || strings.HasPrefix(val, "@") || strings.HasSuffix(val, "@") {
				fmt.Printf("\n要写成 用户名@主机，且两侧都不能为空。\n")
				pauseKey()
				return
			}
			nt.Login = val
		case "2":
			fmt.Printf("    config 名称（直接回车取消）: ")
			val := strings.TrimSpace(readLine())
			if val == "" {
				return
			}
			if strings.Contains(val, "@") {
				fmt.Printf("\n这种方式不应含 @；要指定用户名请选 [1]。\n")
				pauseKey()
				return
			}
			nt.Login = val
		default:
			fmt.Printf("\n请输入 1 或 2。\n")
			pauseKey()
			return
		}
	default:
		fmt.Printf("\n没有 %d 号字段。\n", fno)
		pauseKey()
		return
	}

	if err := replaceConfLine(n, nt.format()); err != nil {
		fmt.Printf("\n修改失败：%v\n", err)
	} else {
		fmt.Printf("\n已修改第 %d 条。\n", n)
	}
	pauseKey()
}

func doDel() {
	clr()
	fmt.Printf("删除配置（编号 = 配置文件行序号；只删配置，不断隧道）\n\n")
	avail := printConfList()
	if len(avail) == 0 {
		fmt.Printf("  配置里还没有隧道。\n")
		pauseKey()
		return
	}

	fmt.Printf("\n输入要删除的编号（直接回车取消）: ")
	n, ok := atoiOK(readLine())
	if !ok {
		return
	}
	if !avail[n] {
		fmt.Printf("\n没有第 %d 条。\n", n)
		pauseKey()
		return
	}
	line, err := deleteConfLine(n)
	if err != nil {
		fmt.Printf("\n删除失败：%v\n", err)
	} else {
		fmt.Printf("\n已删除第 %d 条：%s\n", n, line)
	}
	pauseKey()
}

func doConnect() {
	clr()
	fmt.Printf("连接隧道（编号 = 配置文件行序号）\n\n")
	avail := printConfList()
	if len(avail) == 0 {
		fmt.Printf("  配置里还没有隧道，先按 n 新建。\n")
		pauseKey()
		return
	}

	fmt.Printf("\n输入序号连接（直接回车取消）: ")
	n, ok := atoiOK(readLine())
	if !ok {
		return
	}
	if !avail[n] {
		fmt.Printf("\n没有第 %d 条。\n", n)
		pauseKey()
		return
	}
	fmt.Printf("\n")
	if err := connectTunnel(n); err != nil {
		fmt.Printf("连接失败：%v\n", err)
	}
	pauseKey()
}

func doDisconnect() {
	clr()
	fmt.Printf("断开隧道（编号 = 现有隧道，含外部）\n\n")
	ls, err := scanTunnels()
	if err != nil {
		fmt.Printf("  扫描失败：%v\n", err)
		pauseKey()
		return
	}
	desc := confPortDesc()
	for i, t := range ls {
		d := "-"
		if v := desc[portOf(t.Local)]; v != "" {
			d = v
		}
		fmt.Printf("  %2d) [%s] %s -> %s   %s   (pid %d)   (%s)\n", i+1, t.Dir, t.Local, t.Rem, t.Host, t.PID, d)
	}
	if len(ls) == 0 {
		fmt.Printf("  没有检测到隧道。\n")
		pauseKey()
		return
	}

	fmt.Printf("\n输入编号断开（直接回车取消）: ")
	n, ok := atoiOK(readLine())
	if !ok {
		return
	}
	if n < 1 || n > len(ls) {
		fmt.Printf("\n没有第 %d 项。\n", n)
		pauseKey()
		return
	}
	fmt.Printf("\n")
	if err := disconnectTunnel(ls[n-1].PID); err != nil {
		fmt.Printf("断开失败：%v\n", err)
	}
	pauseKey()
}

// ---------- 小工具 ----------

// atoiOK 解析编号；空输入视为取消（ok=false）。
func atoiOK(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func ipOf(addr string) string   { ip, _ := splitAddr(addr); return ip }
func portOf(addr string) string { _, p := splitAddr(addr); return p }

// confByNo 取第 n 条配置。
func confByNo(n int) (Tunnel, bool) {
	rows, _ := listConf()
	for _, r := range rows {
		if r.No != n {
			continue
		}
		return parseLine(r.Raw)
	}
	return Tunnel{}, false
}

// appendConf 追加一条配置。
func appendConf(t Tunnel) error {
	rows, err := listConf()
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		lines = append(lines, r.Raw)
	}
	lines = append(lines, t.format())
	return writeConf(lines)
}

// replaceConfLine 只替换第 n 条，其余行原样保留（0.01 §7.2）。
func replaceConfLine(n int, newLine string) error {
	rows, err := listConf()
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(rows))
	found := false
	for _, r := range rows {
		if r.No == n {
			lines = append(lines, newLine)
			found = true
			continue
		}
		lines = append(lines, r.Raw)
	}
	if !found {
		return fmt.Errorf("没有第 %d 条", n)
	}
	return writeConf(lines)
}

// deleteConfLine 删除第 n 条，返回被删掉的原始行。
func deleteConfLine(n int) (string, error) {
	rows, err := listConf()
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(rows))
	var removed string
	for _, r := range rows {
		if r.No == n {
			removed = r.Raw
			continue
		}
		lines = append(lines, r.Raw)
	}
	if removed == "" {
		return "", fmt.Errorf("没有第 %d 条", n)
	}
	return removed, writeConf(lines)
}
