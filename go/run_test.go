// 起停 / 日志 / pid 的单元测试（纯函数与文件格式部分）。
// 真实的 ssh 起停由命令行在沙箱目录里端到端验证（不放进单测，避免单测真去建隧道）。
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestClassifyConnectFail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"bind: Address already in use", "本机端口被占"},
		{"Warning: remote port forwarding failed for listen port 8080", "远端端口被占"},
		{"ssh: Could not resolve hostname nope: nodename nor servname provided, or not known", "主机名解析不了"},
		{"ssh: connect to host 10.0.0.9 port 22: Connection refused", "网络不通"},
		{"ssh: connect to host example.com port 22: Operation timed out", "网络不通"},
		{"changwei@x1c: Permission denied (publickey).", "认证失败"},
		{"Host key verification failed.", "host key 未信任"},
		// 真实场景：先有警告行，报错在最后 —— 必须命中的是报错那行
		{"Warning: Permanently added 'x1c' (ED25519) to the list of known hosts.\nssh: connect to host x1c port 22: Connection refused", "网络不通"},
	}
	for _, c := range cases {
		got, ok := classifyConnectFail(c.in)
		if !ok || got != c.want {
			t.Errorf("classifyConnectFail(%q) = %q,%v；期望 %q", c.in, got, ok, c.want)
		}
	}
}

// 都不匹配 → 原样透传最后一行非空内容；整段为空 → 给一句说明（不能返回空字符串）。
func TestConnectFailReasonPassthrough(t *testing.T) {
	in := "Warning: Permanently added 'x' (ED25519) to the list of known hosts.\nssh: some brand new error we do not know"
	if got := connectFailReason(in); got != "ssh: some brand new error we do not know" {
		t.Errorf("应原样透传最后一行，实际 %q", got)
	}
	if got := connectFailReason("   \n\n"); got == "" {
		t.Error("空输出也要给出说明文字，不能返回空")
	}
}

// 0.02 §5：列义固定，只在拼命令行时交换。
func TestSshArgsSwapRule(t *testing.T) {
	l := Tunnel{Dir: "L", Local: "127.0.0.1:3081", Rem: "127.0.0.1:3080", Login: "x1c"}
	args, cmd := sshArgsFor(l)
	want := []string{"-N", "-L", "127.0.0.1:3081:127.0.0.1:3080", "x1c"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("L 方向参数错误：%v", args)
	}
	if cmd != "ssh -N -L 127.0.0.1:3081:127.0.0.1:3080 x1c" {
		t.Errorf("命令行错误：%s", cmd)
	}

	r := Tunnel{Dir: "R", Local: "127.0.0.1:3081", Rem: "10.0.0.5:8080", Login: "changwei@x1c"}
	args, _ = sshArgsFor(r)
	want = []string{"-N", "-R", "10.0.0.5:8080:127.0.0.1:3081", "changwei@x1c"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("R 方向应交换成 列4:列3，实际：%v", args)
	}
}

// 日志一行六段（0.01 §4.3）：时间 / 动作 / 结果 / 本地 -> 远程 / 登录 / pid；空段记 `-`。
func TestAppendLogFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".sshtp.log")
	if err := appendLog(p, "连接", "成功", "127.0.0.1:3081", "127.0.0.1:3080", "x1c", 1287); err != nil {
		t.Fatal(err)
	}
	// 断开时拿不到隧道参数 → 记 `-`，但不能少写段
	if err := appendLog(p, "断开", "失败：本机端口被占", "127.0.0.1:3081", "", "", 0); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("应有 2 行，实际 %d 行", len(lines))
	}

	ts := `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}  `
	if !regexp.MustCompile(ts).MatchString(lines[0]) {
		t.Errorf("第 1 行时间戳形状不对：%q", lines[0])
	}
	want1 := "连接  成功  127.0.0.1:3081 -> 127.0.0.1:3080  x1c  (pid 1287)"
	if !strings.HasSuffix(lines[0], want1) {
		t.Errorf("第 1 行应为 %q，实际 %q", want1, lines[0])
	}
	want2 := "断开  失败：本机端口被占  127.0.0.1:3081 -> -  -  (pid -)"
	if !strings.HasSuffix(lines[1], want2) {
		t.Errorf("第 2 行应为 %q，实际 %q", want2, lines[1])
	}
}

// pid 文件是「当前状态」：多行、断开删一行、空了删文件（0.01 §10.20）。
func TestPidFileLifecycle(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".sshtp.pid")

	if err := pidAddTo(p, 111); err != nil {
		t.Fatal(err)
	}
	if err := pidAddTo(p, 222); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "111\n222\n" {
		t.Fatalf("pid 文件内容应为两行，实际 %q", b)
	}

	if err := pidRemoveFrom(p, 111); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if string(b) != "222\n" {
		t.Fatalf("删掉 111 后应只剩 222，实际 %q", b)
	}

	if err := pidRemoveFrom(p, 222); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("最后一条断开后 pid 文件本身应被删除")
	}

	// 重复删 / 文件不存在：都不该报错
	if err := pidRemoveFrom(p, 333); err != nil {
		t.Errorf("文件不存在时不该报错：%v", err)
	}
}
