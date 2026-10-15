// sshtp —— SSH 隧道管理器（Go 版，0.02）
//
// 本文件是骨架阶段的第一个提交：CLI 外壳 + 配置读写 core。
// 尚未实现（后续提交）：状态扫描、隧道起停、日志、TUI、HTTP —— 代码内以 TODO 标出。
//
// 分层（对齐 0.01 §10.8「一份 core、多个前端」）：
//
//	core —— 配置解析/写回 + 隧道起停 + 状态扫描 + 日志（无任何交互）
//	前端 —— CLI / TUI / HTTP
//
// 配置文件（0.02 §4/§5）：.tunnel.conf，隐藏文件，每行 5 列逗号分隔，列义固定：
//
//	<方向>,<说明>,<本地ip:端口>,<远程ip:端口>,<登录>
//
// 列义不随方向变：第 3 列永远指本机、第 4 列永远指远端；只在拼命令行时交换：
//
//	L → ssh -N -L <列3>:<列4> <列5>
//	R → ssh -N -R <列4>:<列3> <列5>
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	confName  = ".tunnel.conf" // 0.02 §4：隧道列表（隐藏文件、5 列）
	tickSec   = 5             // 0.01 §5.4：TUI 不按键则每 5 秒重绘
	keyWindow = 3 * time.Second
)

// ---------- core：配置模型 ----------

// Tunnel 是一条隧道配置。
type Tunnel struct {
	Dir   string // 列1 方向：L / R；留空 = L
	Desc  string // 列2 说明；留空 = 按行序号自动填 01、02…
	Local string // 列3 本地 ip:端口 —— 永远指本机
	Rem   string // 列4 远程 ip:端口 —— 永远指远端
	Login string // 列5 user@host（带 @ 显式用户名）或 host（走 ~/.ssh/config）
}

// confPath 返回配置文件路径。程序与本文件同目录、扁平放置（0.01 §1）。
func confPath() string {
	exe, err := os.Executable()
	if err != nil {
		return confName
	}
	return filepath.Join(filepath.Dir(exe), confName)
}

// splitAddr 拆 ip:端口。只有端口时 ip 记 127.0.0.1（对齐 bash addr_ip/addr_port）。
func splitAddr(s string) (ip, port string) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "127.0.0.1", s
}

// isPort 校验 1-65535 的纯数字。
func isPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

// parseLine 解析一行配置：按逗号切分必须恰好 5 段，段内首尾空白去掉。
// 段数不对返回 false（该行为无效行：跳过、不显示、不能连接，但仍占一个行序号 —— 0.01 §6）。
func parseLine(raw string) (Tunnel, bool) {
	parts := strings.Split(raw, ",")
	if len(parts) != 5 {
		return Tunnel{}, false
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	t := Tunnel{Dir: parts[0], Desc: parts[1], Local: parts[2], Rem: parts[3], Login: parts[4]}
	if t.Dir == "" {
		t.Dir = "L" // 0.02 §5：方向留空 = L
	}
	return t, true
}

// format 把一条配置拼回一行（5 列）。
func (t Tunnel) format() string {
	return strings.Join([]string{t.Dir, t.Desc, t.Local, t.Rem, t.Login}, ",")
}

// confRow 是配置里的一行：序号（跳过空行与 # 注释后的行号）+ 原始文本。
type confRow struct {
	No  int
	Raw string
}

// listConf 读出全部有效配置行。文件不存在时返回空。
func listConf() ([]confRow, error) {
	b, err := os.ReadFile(confPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rows []confRow
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		n++
		rows = append(rows, confRow{No: n, Raw: line})
	}
	return rows, nil
}

// writeConf 覆盖写回配置文件（只替换目标行时，其余行原样保留 —— 0.01 §7.2）。
func writeConf(lines []string) error {
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	return os.WriteFile(confPath(), []byte(body), 0o644)
}

// ---------- 两个视图（纯输出，TUI 与 CLI 共用同一份） ----------

// showStatus 打印状态表。TODO：扫描尚未实现，暂只打印表头与占位提示。
func showStatus() {
	fmt.Printf("================ sshtp ================\n")
	fmt.Printf("编号 | 方向 | 本地ip:端口 | 转发目标 | SSH主机 | 持续时间 | 通讯量 | 来源 | PID | 说明\n")
	// TODO(0.01 §5.2 / 0.02 §2)：扫描全机 ssh 隧道（lsof+ps 的 Go 等价实现），
	// 按 pid 升序编号，join 说明与通讯量；三道数据源互相独立，拿不到记 "-"。
	fmt.Printf("（扫描尚未实现：Go 版骨架阶段）\n")
	fmt.Printf("\n")
}

// showConfig 打印配置表（编号 | 方向 | 本地 | 转发目标 | SSH主机 | 说明 | 状态）。
func showConfig() {
	fmt.Printf("================ 配置 ================\n")
	fmt.Printf("编号 | 方向 | 本地ip:端口 | 转发目标 | SSH主机 | 说明 | 状态\n")
	rows, err := listConf()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取配置失败：%v\n", err)
		return
	}
	any := false
	for _, r := range rows {
		t, ok := parseLine(r.Raw)
		if !ok {
			continue // 无效行：跳过（但仍占行序号）
		}
		any = true
		// TODO(0.01 §5.2 / 0.02 §2)：这里要判断该本地端口是否已在运行，
		// 已运行输出「已运行 pid N」，否则「未运行」——依赖扫描实现。
		st := "未运行"
		fmt.Printf("%d | %s | %s | %s | %s | %s | %s\n", r.No, t.Dir, t.Local, t.Rem, t.Login, t.Desc, st)
	}
	if !any {
		fmt.Printf("（配置里还没有隧道）\n")
	}
	fmt.Printf("\n")
}

// printKeys 打印键位速查表 —— 本程序唯一的完整说明（0.01 §5.1）。
func printKeys() {
	fmt.Printf("  sshtp —— SSH 隧道管理器（CLI / TUI 同一套键）\n")
	fmt.Printf("  CLI：sshtp <键> [参数]，跑完即退      TUI：在此按键进菜单，不按键每 %d 秒刷新\n", tickSec)
	fmt.Printf("\n")
	fmt.Printf("  配置   [v] 查看配置\n")
	fmt.Printf("         [n] 新建 <说明> <本地ip:端口> <远程ip:端口> <user@host>\n")
	fmt.Printf("         [e] 修改 <编号> <字段> <新值>      [x] 删除 <编号>\n")
	fmt.Printf("  隧道   [c] 连接 <编号>      [d] 断开 <编号>\n")
	fmt.Printf("  其他   [r] 刷新（仅 TUI）    [q] 退出（仅 TUI）\n")
	fmt.Printf("\n")
	fmt.Printf("  编号：v/e/x/c 用配置行号，d 用状态表编号\n")
	fmt.Printf("\n")
}

// ---------- CLI 前端：用法与错误（对齐 bash usage / 各 cli_*） ----------

func usage() {
	fmt.Fprintf(os.Stderr, "用法（键位与 TUI 菜单一致）：\n")
	fmt.Fprintf(os.Stderr, "  sshtp          状态 + 键位与参数形状；3 秒内按任意键进 TUI 菜单，否则退出\n")
	fmt.Fprintf(os.Stderr, "  sshtp v        查看配置\n")
	fmt.Fprintf(os.Stderr, "  sshtp n <说明> <本地ip:端口> <远程ip:端口> <user@host>\n")
	fmt.Fprintf(os.Stderr, "  sshtp e <编号> <字段> <新值>\n")
	fmt.Fprintf(os.Stderr, "  sshtp x <编号>\n")
	fmt.Fprintf(os.Stderr, "  sshtp c <编号>\n")
	fmt.Fprintf(os.Stderr, "  sshtp d <编号>\n")
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// ---------- 入口 ----------

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		noArgs()
		return
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "v":
		showConfig()
	case "n":
		cliNew(rest)
	case "e":
		cliEdit(rest)
	case "x":
		cliDel(rest)
	case "c":
		cliConnect(rest)
	case "d":
		cliDisconnect(rest)
	case "h", "help", "-h", "--help":
		fmt.Fprintf(os.Stderr, "本程序没有 help 子命令：直接敲 sshtp（无参数）即打印全部功能、参数与字段名。\n")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令：%s\n", cmd)
		fmt.Fprintf(os.Stderr, "（直接敲 sshtp（无参数）即打印全部功能、参数与字段名）\n")
		os.Exit(1)
	}
}

// noArgs 无参数入口：打印状态 + 键位表，然后开 3 秒按键窗口（0.01 §5.1）。
func noArgs() {
	showStatus()
	printKeys()
	fmt.Printf("> ")
	if _, ok := waitKey(keyWindow); ok {
		// TODO(0.01 §5.3)：按键直接进 TUI 菜单。骨架阶段 TUI 尚未实现。
		fmt.Fprintf(os.Stderr, "TUI 尚未实现：Go 版骨架阶段。\n")
	}
	fmt.Printf("\n")
}

// waitKey 在超时前读一个字节。stdin 不是终端（管道 / </dev/null）时立即返回。
func waitKey(d time.Duration) (byte, bool) {
	type res struct {
		b  byte
		ok bool
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, 1)
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			ch <- res{0, false} // EOF / 读失败：立即返回，不白等
			return
		}
		ch <- res{buf[0], true}
	}()
	select {
	case r := <-ch:
		return r.b, r.ok
	case <-time.After(d):
		return 0, false
	}
}

// ---------- CLI 前端：配置类动作 ----------

// cliNew 新建配置：sshtp n <说明> <本地ip:端口> <远程ip:端口> <user@host>
// TODO(0.02 §5)：CLI 的 n 暂不接收方向；方向参数形式待定（0.02 §2.1 只要求能区分）。
func cliNew(a []string) {
	if len(a) != 4 {
		fmt.Fprintf(os.Stderr, "参数不齐：需要 4 个（说明 本地ip:端口 远程ip:端口 user@host），收到 %d 个\n", len(a))
		usage()
		os.Exit(1)
	}
	desc, c3, c4, c5 := a[0], a[1], a[2], a[3]
	lip, lport := splitAddr(c3)
	rip, rport := splitAddr(c4)
	if !isPort(lport) {
		die("本地端口无效：%s", lport)
	}
	if !isPort(rport) {
		die("远程端口无效：%s", rport)
	}
	if c5 == "" {
		die("第 5 段不能为空（形如 user@host 或 host）")
	}
	for _, v := range []string{desc, c3, c4, c5} {
		if strings.Contains(v, ",") {
			die("任何一段都不能含逗号（逗号是列分隔符）")
		}
	}
	rows, _ := listConf()
	if desc == "" {
		desc = fmt.Sprintf("%02d", len(rows)+1)
	}
	if lip == "" {
		lip = "127.0.0.1"
	}
	if rip == "" {
		rip = "127.0.0.1"
	}
	t := Tunnel{Dir: "L", Desc: desc, Local: lip + ":" + lport, Rem: rip + ":" + rport, Login: c5}
	lines := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		lines = append(lines, r.Raw)
	}
	lines = append(lines, t.format())
	if err := writeConf(lines); err != nil {
		die("写入失败：%v", err)
	}
	fmt.Printf("已写入 %s\n", confPath())
}

// cliDel 删除配置：sshtp x <编号>
func cliDel(a []string) {
	if len(a) != 1 {
		fmt.Fprintf(os.Stderr, "参数不齐：需要 1 个（编号）\n")
		usage()
		os.Exit(1)
	}
	n, err := strconv.Atoi(a[0])
	if err != nil {
		die("编号无效：%s", a[0])
	}
	rows, _ := listConf()
	var target string
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.No == n {
			target = r.Raw
			continue
		}
		out = append(out, r.Raw)
	}
	if target == "" {
		die("没有第 %d 条配置", n)
	}
	if err := writeConf(out); err != nil {
		die("删除失败：%v", err)
	}
	fmt.Printf("已删除第 %d 条：%s\n", n, target)
}

// cliEdit 修改配置：sshtp e <编号> <字段> <新值>
// 可改字段（0.01 §7.2）：desc lip lport rip rport login（另保留 ruser / rhost）。
func cliEdit(a []string) {
	if len(a) != 3 {
		fmt.Fprintf(os.Stderr, "参数不齐：需要 3 个（编号 字段 新值），收到 %d 个\n", len(a))
		usage()
		os.Exit(1)
	}
	n, err := strconv.Atoi(a[0])
	if err != nil {
		die("编号无效：%s", a[0])
	}
	field, val := a[1], a[2]
	switch field {
	case "desc", "lip", "lport", "rip", "rport", "login", "ruser", "rhost":
	default:
		die("未知字段：%s（可用：desc lip lport rip rport login ruser rhost）", field)
	}
	if val == "" {
		switch field {
		case "lip", "rip":
			val = "127.0.0.1"
		default:
			die("字段 %s 不能为空", field)
		}
	}
	if field == "lport" || field == "rport" {
		if !isPort(val) {
			die("端口无效：%s", val)
		}
	}
	if strings.Contains(val, ",") {
		die("新值不能含逗号（逗号是列分隔符）")
	}

	rows, _ := listConf()
	found := false
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.No != n {
			out = append(out, r.Raw)
			continue
		}
		t, ok := parseLine(r.Raw)
		if !ok {
			die("第 %d 条格式不正确", n)
		}
		lip, lport := splitAddr(t.Local)
		rip, rport := splitAddr(t.Rem)
		ruser, rhost := "", t.Login
		if i := strings.Index(t.Login, "@"); i >= 0 {
			ruser, rhost = t.Login[:i], t.Login[i+1:]
		}
		switch field {
		case "desc":
			t.Desc = val
		case "lip":
			lip = val
		case "lport":
			lport = val
		case "rip":
			rip = val
		case "rport":
			rport = val
		case "ruser":
			ruser = val
		case "rhost":
			rhost = val
		case "login":
			t.Login = val
		}
		if lip == "" {
			lip = "127.0.0.1"
		}
		if rip == "" {
			rip = "127.0.0.1"
		}
		if field != "login" {
			if ruser != "" {
				t.Login = ruser + "@" + rhost
			} else {
				t.Login = rhost
			}
		}
		t.Local = lip + ":" + lport
		t.Rem = rip + ":" + rport
		out = append(out, t.format())
		found = true
	}
	if !found {
		die("没有第 %d 条配置", n)
	}
	if err := os.WriteFile(confPath(), []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		die("修改失败：%v", err)
	}
	fmt.Printf("已修改第 %d 条的 %s\n", n, field)
}

// cliConnect 连接：sshtp c <编号> —— 依赖隧道起停，待后续提交实现。
func cliConnect(a []string) {
	if len(a) != 1 {
		fmt.Fprintf(os.Stderr, "参数不齐：需要 1 个（编号）\n")
		usage()
		os.Exit(1)
	}
	die("连接尚未实现：Go 版骨架阶段")
}

// cliDisconnect 断开：sshtp d <编号> —— 依赖状态扫描，待后续提交实现。
func cliDisconnect(a []string) {
	if len(a) != 1 {
		fmt.Fprintf(os.Stderr, "参数不齐：需要 1 个（编号）\n")
		usage()
		os.Exit(1)
	}
	die("断开尚未实现：Go 版骨架阶段")
}
