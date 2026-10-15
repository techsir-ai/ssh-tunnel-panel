//go:build !windows

// 扫描解析的单元测试。
// 用例里的 lsof / ps 输出按真实格式构造（macOS），用于锁定解析行为，
// 这样在没有真实隧道可测的环境里也能验证移植是否忠实。
package main

import "testing"

func TestSplitSpec(t *testing.T) {
	cases := []struct {
		name, spec, dir, loc, rem string
		ok                        bool
	}{
		{"三段-L", "3081:127.0.0.1:3080", "L", "127.0.0.1:3081", "127.0.0.1:3080", true},
		{"四段-L", "127.0.0.1:3081:127.0.0.1:3080", "L", "127.0.0.1:3081", "127.0.0.1:3080", true},
		{"四段-R需换回", "127.0.0.1:3080:127.0.0.1:3081", "R", "127.0.0.1:3081", "127.0.0.1:3080", true},
		{"三段-R需换回", "3080:127.0.0.1:3081", "R", "127.0.0.1:3081", "127.0.0.1:3080", true},
		{"空规格", "", "L", "", "", false},
		{"段数不足", "a:b", "L", "", "", false},
		{"段数过多", "a:b:c:d:e", "L", "", "", false},
	}
	for _, c := range cases {
		loc, rem, ok := splitSpec(c.spec, c.dir)
		if ok != c.ok || loc != c.loc || rem != c.rem {
			t.Errorf("%s: splitSpec(%q,%q) = (%q,%q,%v)，期望 (%q,%q,%v)",
				c.name, c.spec, c.dir, loc, rem, ok, c.loc, c.rem, c.ok)
		}
	}
}

func TestForwardSpec(t *testing.T) {
	cases := []struct {
		name, want, dir string
		args            []string
	}{
		{"-L 分开写", "3081:127.0.0.1:3080", "L", []string{"-N", "-L", "3081:127.0.0.1:3080", "x1c"}},
		{"-L 贴写", "3081:127.0.0.1:3080", "L", []string{"-N", "-L3081:127.0.0.1:3080", "x1c"}},
		{"-R 分开写", "3080:127.0.0.1:3081", "R", []string{"-N", "-R", "3080:127.0.0.1:3081", "x1c"}},
		{"-o LocalForward=", "3081:127.0.0.1:3080", "L", []string{"-o", "LocalForward=3081:127.0.0.1:3080", "x1c"}},
		{"裸 LocalForward=", "3081:127.0.0.1:3080", "L", []string{"LocalForward=3081:127.0.0.1:3080"}},
		{"-o RemoteForward=", "3080:127.0.0.1:3081", "R", []string{"-o", "RemoteForward=3080:127.0.0.1:3081", "x1c"}},
		{"无转发", "", "", []string{"-N", "x1c"}},
	}
	for _, c := range cases {
		spec, dir := forwardSpec(c.args)
		if spec != c.want || dir != c.dir {
			t.Errorf("%s: forwardSpec(%v) = (%q,%q)，期望 (%q,%q)", c.name, c.args, spec, dir, c.want, c.dir)
		}
	}
}

func TestParseLsof(t *testing.T) {
	out := `COMMAND     PID     USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
ssh        1287 changwei    3u  IPv4 0x1234567890abcdef      0t0  TCP 127.0.0.1:3081 (LISTEN)
python3     999 changwei    4u  IPv4 0xabcdef0000000000      0t0  TCP 127.0.0.1:8000 (LISTEN)
ssh         555 changwei    5u  IPv6 0xdeadbeefdeadbeef      0t0  TCP *:9000 (LISTEN)
`
	got := parseLsof(out)
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条 ssh 监听，实际 %d 条：%v", len(got), got)
	}
	if got[1287] != "127.0.0.1:3081" {
		t.Errorf("pid 1287 监听地址 = %q，期望 127.0.0.1:3081", got[1287])
	}
	if got[555] != "*:9000" {
		t.Errorf("pid 555 监听地址 = %q，期望 *:9000", got[555])
	}
}

func TestParsePs(t *testing.T) {
	out := `  1287 03:07:41 /usr/bin/ssh -N -L 3081:127.0.0.1:3080 x1c
  1288 01:02:03 /usr/bin/ssh -N -L127.0.0.1:9000:127.0.0.1:8080 y
 12890  2-11:00:09 ssh -o LocalForward=3081:127.0.0.1:3080 changwei@z1c
  9999 00:10:00 /usr/bin/python3 -m http.server 8000
`
	rows := parsePs(out)
	if len(rows) != 3 {
		t.Fatalf("应解析出 3 条 ssh 进程，实际 %d 条：%+v", len(rows), rows)
	}
	if rows[0].PID != 1287 || rows[0].Spec != "3081:127.0.0.1:3080" || rows[0].Dir != "L" || rows[0].Host != "x1c" {
		t.Errorf("第 1 条解析错误：%+v", rows[0])
	}
	if rows[0].Etime != "03:07:41" {
		t.Errorf("持续时间 = %q，期望 03:07:41", rows[0].Etime)
	}
	if rows[1].Spec != "127.0.0.1:9000:127.0.0.1:8080" || rows[1].Dir != "L" || rows[1].Host != "y" {
		t.Errorf("第 2 条（贴写）解析错误：%+v", rows[1])
	}
	if rows[2].PID != 12890 || rows[2].Spec != "3081:127.0.0.1:3080" || rows[2].Host != "changwei@z1c" {
		t.Errorf("第 3 条（LocalForward=）解析错误：%+v", rows[2])
	}
	if rows[2].Etime != "2-11:00:09" {
		t.Errorf("长时间格式 = %q，期望 2-11:00:09", rows[2].Etime)
	}
}

func TestMergeScanByPID(t *testing.T) {
	// pid 1287：两路都有 —— 命令行带 -L 的普通隧道
	// pid 555 ：只有 lsof —— 隧道来自 ~/.ssh/config 的 LocalForward，命令行里没有 -L，
	//           所以 ps 解析不出转发目标，但 lsof 能看到监听端口（0.01 §10.6 的关键点）
	// pid 999 ：两路都没有转发证据（普通 ssh 登录，不是隧道）→ 必须排除
	listen := map[int]string{
		1287: "127.0.0.1:3081",
		555:  "127.0.0.1:9000",
	}
	rows := []psRow{
		{PID: 1287, Etime: "03:07:41", Spec: "3081:127.0.0.1:3080", Dir: "L", Host: "x1c"},
		{PID: 555, Etime: "01:00:00", Spec: "", Dir: "L", Host: "y"},
		{PID: 999, Etime: "00:01:00", Spec: "", Dir: "L", Host: "z"},
	}
	got := mergeScan(listen, rows)
	if len(got) != 2 {
		t.Fatalf("应合并出 2 条隧道（999 不是隧道），实际 %d 条：%+v", len(got), got)
	}
	if got[0].PID != 555 || got[1].PID != 1287 {
		t.Errorf("应按 pid 升序排列，实际顺序：%d, %d", got[0].PID, got[1].PID)
	}
	if got[1].Local != "127.0.0.1:3081" || got[1].Rem != "127.0.0.1:3080" || got[1].Host != "x1c" {
		t.Errorf("两路都有的那条合并结果错误：%+v", got[1])
	}
	// 只有 lsof 的那条：本地监听取 lsof（权威），转发目标记 "-"，整行照常输出
	if got[0].Local != "127.0.0.1:9000" || got[0].Rem != "-" || got[0].Host != "y" {
		t.Errorf("只有 lsof 的那条应为本地址来自 lsof、目标记 -，实际：%+v", got[0])
	}
}

// 两路都拿不到 → 必须排除，不能靠"进程是 ssh"就当成隧道
func TestMergeScanExcludesNonTunnel(t *testing.T) {
	rows := []psRow{{PID: 999, Etime: "00:01:00", Spec: "", Dir: "L", Host: "z"}}
	got := mergeScan(map[int]string{}, rows)
	if len(got) != 0 {
		t.Errorf("没有任何转发证据时不应产生记录，实际：%+v", got)
	}
}
