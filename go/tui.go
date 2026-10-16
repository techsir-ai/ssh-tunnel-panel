// TUI 前端 —— 交互（清屏 / 读键 / 提问）。
//
// 与 0.01 §5.3 / §5.4 对齐：
//   - 整屏重绘（清屏 + 重印），不移动光标、不做局部刷新
//   - 不引入 curses / whiptail / dialog 等任何外部库
//   - 不按键则每 tickSec 秒自动刷新一次（状态表含"持续时间/通讯量"，是活的量）
//   - 退出（q）= 只退出程序，不改变任何隧道状态
//
// 键位与 CLI 完全一致：同一个键，TUI 里是「交互 → 执行」，CLI 里是「一次性执行 → 退出」，
// 差的只是这个 while 循环。
//
// 输入细节（行尾、回显、全角字符、非法输入）全部收在 input.go，这里只负责问什么、怎么问。
package main

import (
	"fmt"
	"strings"
	"time"
)

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

		switch normKey(key) {
		case 'r':
			// 刷新 = 重绘（每轮循环本就整屏重绘）；把它显式写进键位表是给人一个明确的动作
		case 'v':
			doView()
		case 'n':
			doNew()
		case 'e':
			doEdit()
		case 'x':
			doDel()
		case 'c':
			doConnect()
		case 'd':
			doDisconnect()
		case 'q':
			return
		}
	}
}

// normKey 把按键归一化：大写折小写，全角折半角（中文输入法常给全角）。
// 其余按键返回 0（不会匹配任何 case，等于重绘）。
func normKey(b byte) byte {
	switch {
	case b >= 'A' && b <= 'Z':
		return b - 'A' + 'a'
	case b >= 0x80: // 可能是全角字符 / 汉字的首字节：补全后归一化
		s := append([]byte{b}, readContinuation()...)
		n := normalizeASCII(string(s))
		if len(n) == 1 && n[0] < 0x80 {
			return normKey(n[0]) // 只归一化成一个 ASCII 字节时才递归，不会死循环
		}
		return 0
	}
	return b
}

// readContinuation 补读 UTF-8 后续字节（不等待：终端通常一次给全）。
// 只看不动：遇到非后续字节就收手，绝不打乱输入次序。
func readContinuation() []byte {
	var out []byte
	for i := 0; i < 3; i++ {
		nb, ok := peekKey(10 * time.Millisecond)
		if !ok {
			break
		}
		if nb < 0x80 || nb > 0xBF {
			break
		}
		out = append(out, nb)
		pushback = pushback[1:]
	}
	return out
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
	skipLeadingNewlines()
	clr()
	showConfig()
	pauseKey()
}

func doNew() {
	skipLeadingNewlines() // 菜单上那一下回车可能残留 CR/LF，先排掉，免得替用户答掉第一问（别的按键保留）
	clr()
	fmt.Printf("新建（逐项输入；此行只写配置，不自动连接）\n\n")

	// 1) 方向（0.02 §2.1：配置与界面都要能区分 L / R）—— 单键即选，回车取默认
	fmt.Printf(" 1) 方向\n")
	fmt.Printf("    [1] L  本地转发（本机监听 → 转发到远端）  ← 默认\n")
	fmt.Printf("    [2] R  反向转发（远端监听 → 转发到本机）\n")
	ans, eof := askChoice("[1] L（默认） / [2] R", "", "1", "2")
	if eof {
		return
	}
	dir := "L"
	if ans == "2" {
		dir = "R"
	}

	rows, _ := listConf()
	def := fmt.Sprintf("%02d", len(rows)+1)
	fmt.Printf("\n 2) 说明 —— 这条隧道的备注。任意文本、可含空格；不能含逗号（逗号是列分隔符）\n")
	fmt.Printf("    留空则自动使用序号：%s\n", def)
	desc, eof := askPrompt("    > ")
	if eof {
		return
	}
	if desc == "" {
		desc = def
	}

	fmt.Printf("\n 3) 本地 IP —— 本机监听地址，如 127.0.0.1 或 192.168.10.100\n")
	fmt.Printf("    留空记 127.0.0.1\n")
	lip, eof := askPromptASCII("    > ")
	if eof {
		return
	}
	if lip == "" {
		lip = "127.0.0.1"
	}

	lport, eof := askPort("\n 4) 本地端口 —— 本机监听端口，如 3081（必填）\n", "    > ", false)
	if eof {
		return
	}

	fmt.Printf("\n 5) 远程 IP —— 从远端视角解析，如 127.0.0.1 或 10.0.0.5\n")
	fmt.Printf("    留空记 127.0.0.1\n")
	rip, eof := askPromptASCII("    > ")
	if eof {
		return
	}
	if rip == "" {
		rip = "127.0.0.1"
	}

	rport, eof := askPort("\n 6) 远程端口 —— 目标端口，如 3080（留空 = 取本地端口）\n", "    > ", true)
	if eof {
		return
	}
	if rport == "" {
		rport = lport // 0.02 §2.2
	}

	fmt.Printf("\n 7) 远程登录方式\n")
	fmt.Printf("    [1] user@host     显式指定用户名，如 changwei@x1c\n")
	fmt.Printf("    [2] config 名称    走 ~/.ssh/config，不写用户名，如 x1c\n")
	mode, eof := askChoice("[1] user@host / [2] config 名称", "1", "2")
	if eof {
		return
	}

	var login string
	if mode == "1" {
		fmt.Printf("\n 8) 用户名@主机\n")
		for {
			v, e := askPromptASCII("    > ")
			if e {
				return
			}
			if v == "" {
				fmt.Printf("    不能为空。\n")
				continue
			}
			if !strings.Contains(v, "@") {
				fmt.Printf("    这种方式要写成 用户名@主机，如 changwei@x1c。\n")
				continue
			}
			if strings.HasPrefix(v, "@") || strings.HasSuffix(v, "@") {
				fmt.Printf("    用户名和主机都不能为空。\n")
				continue
			}
			login = v
			break
		}
	} else {
		fmt.Printf("\n 8) config 名称 —— ~/.ssh/config 里的 Host 别名\n")
		for {
			v, e := askPromptASCII("    > ")
			if e {
				return
			}
			if v == "" {
				fmt.Printf("    config 名称不能为空。\n")
				continue
			}
			if strings.Contains(v, "@") {
				fmt.Printf("    这种方式不应含 @；要指定用户名请回上一步选 [1]。\n")
				continue
			}
			login = v
			break
		}
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
	skipLeadingNewlines()
	clr()
	fmt.Printf("修改配置（编号 = 配置文件行序号）\n\n")
	avail := printConfList()
	if len(avail) == 0 {
		fmt.Printf("  配置里还没有隧道，先按 n 新建。\n")
		pauseKey()
		return
	}

	n, ok, eof := askInt("\n输入要修改的编号（直接回车取消）: ")
	if eof {
		return
	}
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

	fno, eof := askChoice(
		"[1] 方向 [2] 说明 [3] 本地IP [4] 本地端口 [5] 远程IP [6] 远程端口 [7] 远程登录（回车取消）",
		"", "1", "2", "3", "4", "5", "6", "7")
	if eof {
		return
	}
	if fno == "" {
		return
	}

	nt := t
	switch fno {
	case "1":
		fmt.Printf("\n 新的方向\n")
		fmt.Printf("    [1] L  [2] R\n")
		ans, e := askChoice("[1] L / [2] R（回车取消）", "", "1", "2")
		if e {
			return
		}
		switch ans {
		case "":
			return
		case "1":
			nt.Dir = "L"
		case "2":
			nt.Dir = "R"
		}
	case "2", "3", "4", "5", "6":
		name := map[string]string{"2": "说明", "3": "本地 IP", "4": "本地端口", "5": "远程 IP", "6": "远程端口"}[fno]
		ascii := fno != "2" // 只有「说明」保留中文原样
		var val string
		for {
			fmt.Printf("\n新的%s（直接回车取消）\n", name)
			var v string
			var e bool
			if ascii {
				v, e = askPromptASCII("    > ")
			} else {
				v, e = askPrompt("    > ")
			}
			if e {
				return
			}
			if v == "" {
				return
			}
			if strings.Contains(v, ",") {
				fmt.Printf("    新值不能含逗号（逗号是列分隔符）。\n")
				continue
			}
			if (fno == "4" || fno == "6") && !isPort(v) {
				fmt.Printf("    端口必须是 1-65535 的数字（收到「%s」）。\n", describeInput(v))
				continue
			}
			val = v
			break
		}
		switch fno {
		case "2":
			nt.Desc = val
		case "3":
			nt.Local = val + ":" + portOf(t.Local)
		case "4":
			nt.Local = ipOf(t.Local) + ":" + val
		case "5":
			nt.Rem = val + ":" + portOf(t.Rem)
		case "6":
			nt.Rem = ipOf(t.Rem) + ":" + val
		}
	case "7":
		fmt.Printf("\n 新的远程登录方式\n")
		fmt.Printf("    [1] user@host   [2] config 名称\n")
		ans, e := askChoice("[1] user@host / [2] config 名称（回车取消）", "", "1", "2")
		if e {
			return
		}
		switch ans {
		case "":
			return
		case "1":
			fmt.Printf("    用户名@主机\n")
			for {
				v, e := askPromptASCII("    > ")
				if e {
					return
				}
				if v == "" {
					return
				}
				if !strings.Contains(v, "@") || strings.HasPrefix(v, "@") || strings.HasSuffix(v, "@") {
					fmt.Printf("    要写成 用户名@主机，且两侧都不能为空。\n")
					continue
				}
				nt.Login = v
				break
			}
		case "2":
			fmt.Printf("    config 名称\n")
			for {
				v, e := askPromptASCII("    > ")
				if e {
					return
				}
				if v == "" {
					return
				}
				if strings.Contains(v, "@") {
					fmt.Printf("    这种方式不应含 @；要指定用户名请选 [1]。\n")
					continue
				}
				nt.Login = v
				break
			}
		}
	}

	if err := replaceConfLine(n, nt.format()); err != nil {
		fmt.Printf("\n修改失败：%v\n", err)
	} else {
		fmt.Printf("\n已修改第 %d 条。\n", n)
	}
	pauseKey()
}

func doDel() {
	skipLeadingNewlines()
	clr()
	fmt.Printf("删除配置（编号 = 配置文件行序号；只删配置，不断隧道）\n\n")
	avail := printConfList()
	if len(avail) == 0 {
		fmt.Printf("  配置里还没有隧道。\n")
		pauseKey()
		return
	}

	n, ok, eof := askInt("\n输入要删除的编号（直接回车取消）: ")
	if eof {
		return
	}
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
	skipLeadingNewlines()
	clr()
	fmt.Printf("连接隧道（编号 = 配置文件行序号）\n\n")
	avail := printConfList()
	if len(avail) == 0 {
		fmt.Printf("  配置里还没有隧道，先按 n 新建。\n")
		pauseKey()
		return
	}

	n, ok, eof := askInt("\n输入序号连接（直接回车取消）: ")
	if eof {
		return
	}
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
	skipLeadingNewlines()
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

	n, ok, eof := askInt("\n输入编号断开（直接回车取消）: ")
	if eof {
		return
	}
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

func clr() { clearScreen() }

// pauseKey 等一个键再返回。子流程结束、或出错需要让人看清时用。
func pauseKey() {
	fmt.Print("\n按任意键返回...")
	if _, closed := waitKey(); closed {
		fmt.Print("\n")
		return
	}
	fmt.Print("\n")
}

// ---------- 小工具 ----------

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
