// 状态扫描的共享单元测试 —— 两个平台都会跑。
// splitSpec / forwardSpec / mergeScan / formatEtime 是共用的解析与合并逻辑，
// 平台差异只体现在「谁来产出 listen 与 psRow」这一步。
package main

import (
	"testing"
	"time"
)

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

func TestLastNonFlag(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-N", "-L", "3081:127.0.0.1:3080", "x1c"}, "x1c"},
		{[]string{"-N", "-L", "3081:127.0.0.1:3080", "changwei@x1c"}, "changwei@x1c"},
		{[]string{"-N", "x1c"}, "x1c"},
		{[]string{"-N"}, ""},
		{[]string{}, ""},
	}
	for _, c := range cases {
		if got := lastNonFlag(c.args); got != c.want {
			t.Errorf("lastNonFlag(%v) = %q，期望 %q", c.args, got, c.want)
		}
	}
}

func TestMergeScanByPID(t *testing.T) {
	// pid 1287：两路都有 —— 命令行带 -L 的普通隧道
	// pid 555 ：只有监听端口 —— 隧道来自 ssh_config 的 LocalForward，命令行里没有 -L，
	//           所以解析不出转发目标，但系统查询能看到监听端口（0.01 §10.6 的关键点）
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
	// 只有监听端口的那条：本地监听取系统查询（权威），转发目标记 "-"，整行照常输出
	if got[0].Local != "127.0.0.1:9000" || got[0].Rem != "-" || got[0].Host != "y" {
		t.Errorf("只有监听端口的那条应为本地来自系统查询、目标记 -，实际：%+v", got[0])
	}
}

// 两路都拿不到 → 必须排除，不能靠「进程是 ssh」就当成隧道
func TestMergeScanExcludesNonTunnel(t *testing.T) {
	rows := []psRow{{PID: 999, Etime: "00:01:00", Spec: "", Dir: "L", Host: "z"}}
	got := mergeScan(map[int]string{}, rows)
	if len(got) != 0 {
		t.Errorf("没有任何转发证据时不应产生记录，实际：%+v", got)
	}
}

// 只有监听端口、没有任何进程记录时，也必须输出（否则 ps 受限就会整表变空）
func TestMergeScanOnlyListen(t *testing.T) {
	got := mergeScan(map[int]string{42: "127.0.0.1:3081"}, nil)
	if len(got) != 1 {
		t.Fatalf("只有监听端口时也应输出 1 条，实际 %d 条：%+v", len(got), got)
	}
	if got[0].PID != 42 || got[0].Local != "127.0.0.1:3081" || got[0].Rem != "-" || got[0].Etime != "-" {
		t.Errorf("缺进程记录时应记 -，实际：%+v", got[0])
	}
}

func TestFormatEtime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00:00"},
		{3*time.Hour + 7*time.Minute + 41*time.Second, "03:07:41"},
		{59*time.Minute + 59*time.Second, "00:59:59"},
		{24*time.Hour + 11*time.Hour + 9*time.Second, "1-11:00:09"},
		{2*24*time.Hour + 11*time.Hour + 9*time.Second, "2-11:00:09"},
		{-5 * time.Second, "00:00:00"},
	}
	for _, c := range cases {
		if got := formatEtime(c.d); got != c.want {
			t.Errorf("formatEtime(%v) = %q，期望 %q", c.d, got, c.want)
		}
	}
}
