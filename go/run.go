// core：隧道起停 + 日志 + pid 记录（无任何交互）。
//
// 依据：
//
//	0.01 §4.2  连接 = 后台起 `ssh -N -L`，**不占 console**；起之前先查该本地端口是否已在运行，已运行不重复起
//	0.01 §4.2  断开 = 结束**任意**现有隧道（含非本程序起的），按 pid 指定
//	0.01 §4.3  日志一行六段（时间 / 动作 / 结果 / 本地 -> 远程 / 登录 / pid），拿不到记 `-`，只增不删
//	0.01 §10.3 用 nohup 那一路（不用 `ssh -f`：ssh 自己 daemonize 后拿不到 pid）
//	0.01 §10.20 pid 文件是「当前状态」：起一条追加一行、断开删掉那一行，空了就删文件
//	0.02 §5    命令行只在拼接时交换：L → `ssh -N -L 列3:列4`；R → `ssh -N -R 列4:列3`
//	0.02 §7    连接失败必须报出来；**同一份原因文本**同时进日志和界面；不认识的原样透传
//
// 平台相关部分（进程怎么起得下、怎么停）在 run_unix.go / run_windows.go。
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// connectWindow 起连接后的观察窗口：这期间 ssh 要么退出（失败）、要么报出可判定的错，否则算连上。
// 比 bash 版的固定 1 秒稍长，为的是让「端口被占 / 解析不了 / 拒绝」这类快错都被抓到。
const connectWindow = 1500 * time.Millisecond

// ---------- 路径 ----------

// dataDir 返回程序自身所在目录（0.01 §1：程序 / 配置 / 日志 / pid 全部同级扁平放置）。
func dataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func confPath() string { return filepath.Join(dataDir(), confName) }
func pidPath() string  { return filepath.Join(dataDir(), pidName) }
func logPath() string  { return filepath.Join(dataDir(), logName) }

// ---------- 日志（0.01 §4.3） ----------

// logResult 写一行日志：<时间>  <动作>  <结果>  <本地> -> <远程>  <登录>  (pid N)
// 拿不到某段就记 `-`；**绝不因此少写一段或跳过整行**。
// 结果里带上失败原因（0.02 §7：同一份原因文本也要进日志）。
func logResult(action, result, local, remote, login string, pid int) {
	_ = appendLog(logPath(), action, result, local, remote, login, pid)
}

// appendLog 是 logResult 的可指定路径版本（便于单测）。
func appendLog(path, action, result, local, remote, login string, pid int) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := fmt.Sprintf("%s  %s  %s  %s -> %s  %s  (pid %s)\n",
		time.Now().Format("2006-01-02 15:04:05"),
		dash(action), dash(result), dash(local), dash(remote), dash(login),
		pidField(pid))
	_, err = f.WriteString(line)
	return err
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func pidField(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return fmt.Sprint(pid)
}

// ---------- pid 记录（0.01 §10.20：一个文件、多行、空了就删文件） ----------

func pidAdd(pid int) error                { return pidAddTo(pidPath(), pid) }
func pidRemove(pid int) error             { return pidRemoveFrom(pidPath(), pid) }

func pidAddTo(path string, pid int) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d\n", pid)
	return err
}

// pidRemoveFrom 删掉那一行；删完为空就把文件本身也删掉（目录里不留空壳文件）。
func pidRemoveFrom(path string, pid int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	want := fmt.Sprint(pid)
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || s == want {
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

// ---------- 命令行拼接（0.02 §5：列义固定，只在拼接时交换） ----------

// sshArgsFor 返回 ssh 的参数与可读的命令行。
//
//	L → ssh -N -L <列3>:<列4> <列5>
//	R → ssh -N -R <列4>:<列3> <列5>
func sshArgsFor(t Tunnel) (args []string, cmdline string) {
	spec := t.Local + ":" + t.Rem
	flag := "-L"
	if strings.EqualFold(t.Dir, "R") {
		spec = t.Rem + ":" + t.Local
		flag = "-R"
	}
	args = []string{"-N", flag, spec, t.Login}
	return args, "ssh " + strings.Join(args, " ")
}

// ---------- 连接失败原因（0.02 §7） ----------

// failKeywords 是 0.02 §7 的归类表：ssh 报错关键词 → 判定。
// 顺序即优先级：先命中先算。
var failKeywords = []struct {
	key  string
	text string
}{
	{"address already in use", "本机端口被占"},
	{"remote port forwarding failed", "远端端口被占"},
	{"could not resolve hostname", "主机名解析不了"},
	{"connection refused", "网络不通"},
	{"connection timed out", "网络不通"},
	{"operation timed out", "网络不通"},
	{"permission denied", "认证失败"},
	{"host key verification failed", "host key 未信任"},
	{"no route to host", "网络不通"},
	{"network is unreachable", "网络不通"},
	{"connection closed by", "网络不通"},
}

// classifyConnectFail 按 0.02 §7 归类 ssh 的报错输出；都没命中 → ok=false（调用方原样透传）。
func classifyConnectFail(raw string) (string, bool) {
	// 从最后一行往前找：ssh 的报错通常在最后，前面可能有警告
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.ToLower(strings.TrimSpace(lines[i]))
		if l == "" {
			continue
		}
		for _, k := range failKeywords {
			if strings.Contains(l, k.key) {
				return k.text, true
			}
		}
	}
	return "", false
}

// connectFailReason 给出「同一份原因文本」：命中的用归类文本，没命中的原样透传。
// 日志与界面都用它（0.02 §7）。
func connectFailReason(raw string) string {
	if s, ok := classifyConnectFail(raw); ok {
		return s
	}
	// 原样透传：只取最后一行非空内容，避免把多行警告整段搬上界面
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return "ssh 没有输出错误信息（进程已退出）"
}

// ---------- 起停 ----------

// localPortRunning 该本地监听端口是否已在运行；是则返回 pid（0.01 §4.2：已运行不重复起）。
func localPortRunning(port string) (int, bool) {
	ls, err := scanTunnels()
	if err != nil {
		return 0, false // 扫不到就当没在跑，让 ssh 自己去报「端口被占」
	}
	for _, t := range ls {
		if _, p := splitAddr(t.Local); p == port {
			return t.PID, true
		}
	}
	return 0, false
}

// connectTunnel 连接第 n 条配置：后台起 ssh、记 pid、写日志。
// 失败返回的原因文本与写进日志的**完全一致**（0.02 §7）。
func connectTunnel(n int) error {
	t, ok := confByNo(n)
	if !ok {
		return fmt.Errorf("没有第 %d 条配置", n)
	}
	if _, lport := splitAddr(t.Local); lport != "" {
		if pid, running := localPortRunning(lport); running {
			return fmt.Errorf("该隧道已在运行（pid %d），不重复连接", pid)
		}
	}

	args, cmdline := sshArgsFor(t)
	run, err := startSSH(args)
	if err != nil {
		reason := "启动 ssh 失败：" + err.Error()
		logResult("连接", "失败："+reason, t.Local, t.Rem, t.Login, 0)
		return fmt.Errorf("%s", reason)
	}

	exited, reason := run.observe()
	if reason == "" && exited {
		reason = connectFailReason(run.stderrText())
	}
	if reason != "" {
		run.stop() // 进程可能还活着（例如远端端口被占时 ssh 只警告不退出）
		logResult("连接", "失败："+reason, t.Local, t.Rem, t.Login, run.pid())
		return fmt.Errorf("%s", reason)
	}

	pid := run.pid()
	if err := pidAdd(pid); err != nil {
		// pid 记不上不算连接失败（隧道是活的），但必须说出来
		fmt.Printf("（警告：pid 记录写入失败：%v —— 状态表的「来源」列会显示成外部）\n", err)
	}
	logResult("连接", "成功", t.Local, t.Rem, t.Login, pid)
	fmt.Printf("已连接：%s   (pid %d)\n", cmdline, pid)
	return nil
}

// disconnectTunnel 按 pid 断开隧道（含非本程序起的）。
func disconnectTunnel(pid int) error {
	// 先扫一次拿这条隧道的参数，写进日志（0.01 §10.19：拿不到记 `-`）
	loc, rem, tgt := "-", "-", "-"
	if ls, err := scanTunnels(); err == nil {
		for _, t := range ls {
			if t.PID == pid {
				loc, rem, tgt = t.Local, t.Rem, t.Host
				break
			}
		}
	}

	if !processAlive(pid) {
		// 已经没了（外部停掉 / 早就退出）：按 0.01 的口径仍记「成功」并把 pid 行清掉
		logResult("断开", "成功", loc, rem, tgt, pid)
		_ = pidRemove(pid)
		fmt.Printf("已断开 (pid %d)\n", pid)
		return nil
	}

	if err := killPID(pid); err != nil {
		reason := "结束进程失败：" + err.Error()
		logResult("断开", "失败："+reason, loc, rem, tgt, pid)
		return fmt.Errorf("%s", reason)
	}
	if !waitProcessGone(pid, time.Second) {
		reason := fmt.Sprintf("进程 %d 仍在运行", pid)
		logResult("断开", "失败："+reason, loc, rem, tgt, pid)
		return fmt.Errorf("断开失败：%s", reason)
	}
	logResult("断开", "成功", loc, rem, tgt, pid)
	_ = pidRemove(pid)
	fmt.Printf("已断开 (pid %d)\n", pid)
	return nil
}

func waitProcessGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !processAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---------- ssh 进程 ----------

// sshRun 是一次 ssh 起的进程：它的退出、它的 stderr（用于归类失败原因）。
type sshRun struct {
	cmd    *exec.Cmd
	stderr *syncBuffer
	exited chan struct{}
}

func (r *sshRun) pid() int { return r.cmd.Process.Pid }

// startSSH 后台起 ssh，**不占 console**（0.01 §4.2）：
// stdin/stdout 接空设备，stderr 接管道用来归类失败原因；
// 平台相关的 detach 选项见 detachedAttrs。
func startSSH(args []string) (*sshRun, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("找不到 ssh 命令：%w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer devNull.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(sshPath, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = pw
	cmd.SysProcAttr = detachedAttrs()
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, err
	}
	_ = pw.Close() // 父进程不留写端

	run := &sshRun{cmd: cmd, stderr: &syncBuffer{}, exited: make(chan struct{})}
	// 持续把 ssh 的 stderr 读走：既拿到失败原因，也让 ssh 后面写日志时不会撞上"管道无读者"。
	// 读协程随 ssh 退出（管道关闭）自然结束。
	go func() {
		_, _ = io.Copy(run.stderr, pr)
		_ = pr.Close()
	}()
	go func() {
		_ = cmd.Wait()
		close(run.exited)
	}()
	return run, nil
}

// observe 在 connectWindow 内盯着：进程退出、或 stderr 出现可判定的失败，就提前收工。
// 返回 (是否已退出, 归类出的原因)；原因为空表示这窗口内没看出问题。
//
// 为什么要看 stderr 而不只看进程是否活着：`ssh -N -R` 遇到远端端口被占时**只警告不退出**，
// `-L` 遇到本机端口被占也一样——只看存活会把这些失败报成「已连接」。
func (r *sshRun) observe() (bool, string) {
	deadline := time.Now().Add(connectWindow)
	for {
		select {
		case <-r.exited:
			return true, ""
		default:
		}
		if s, ok := classifyConnectFail(r.stderrText()); ok {
			return false, s
		}
		if time.Now().After(deadline) {
			return false, ""
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *sshRun) stderrText() string { return r.stderr.String() }

// stop 结束这条 ssh（连接被判为失败时收尾用）。
func (r *sshRun) stop() {
	if r == nil || r.cmd == nil || r.cmd.Process == nil {
		return
	}
	_ = killPID(r.pid())
}

// syncBuffer 是带锁的字节缓冲：写方是读 ssh stderr 的协程，读方是主流程。
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > 8192 { // 只留尾部，够归类就行
		b.buf = b.buf[len(b.buf)-8192:]
	}
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

var _ io.Writer = (*syncBuffer)(nil)
