//go:build windows

// Windows 侧取数与解析的单元测试。
// 用例按实机采集的真实输出构造（中文 Windows 的 netstat 表头 + pwsh 的 JSON）。
package main

import (
	"testing"
	"time"
)

func TestParseNetstat(t *testing.T) {
	// 中文 Windows 的表头是本地化的，但状态词 LISTENING 是英文（已实机确认）
	out := `活动连接

  协议  本地地址          外部地址        状态           PID
  TCP    0.0.0.0:22             0.0.0.0:0              LISTENING       8872
  TCP    127.0.0.1:3081         0.0.0.0:0              LISTENING       1287
  TCP    127.0.0.1:9000         0.0.0.0:0              LISTENING       1288
  TCP    127.0.0.1:5000         127.0.0.1:6000         ESTABLISHED     999
  UDP    0.0.0.0:500              0.0.0.0:0                          1234
`
	got := parseNetstat(out)
	if len(got) != 3 {
		t.Fatalf("应解析出 3 条 LISTENING，实际 %d 条：%v", len(got), got)
	}
	if got[8872] != "0.0.0.0:22" {
		t.Errorf("pid 8872 = %q，期望 0.0.0.0:22", got[8872])
	}
	if got[1287] != "127.0.0.1:3081" {
		t.Errorf("pid 1287 = %q，期望 127.0.0.1:3081", got[1287])
	}
	if _, ok := got[999]; ok {
		t.Errorf("ESTABLISHED 行不应被当作监听")
	}
	if _, ok := got[1234]; ok {
		t.Errorf("UDP 行不应被当作 TCP 监听")
	}
}

func TestParseTasklist(t *testing.T) {
	// 有匹配：标准 CSV，注意第 5 段带千分位逗号
	withMatch := `"ssh.exe","1287","Console","1","12,345 K"
"ssh.exe","1288","Console","1","8,900 K"
`
	pids := parseTasklist(withMatch)
	if len(pids) != 2 || pids[0] != 1287 || pids[1] != 1288 {
		t.Errorf("应解析出 [1287 1288]，实际 %v", pids)
	}

	// 无匹配：tasklist 输出一句本地化提示（不是 CSV），必须解析成空集合而不是报错
	if got := parseTasklist("信息: 没有运行的任务匹配指定标准。\r\n"); len(got) != 0 {
		t.Errorf("无匹配时应为空，实际 %v", got)
	}
	if got := parseTasklist(""); len(got) != 0 {
		t.Errorf("空输入应为空，实际 %v", got)
	}
}

func TestParseProcsJSONShapes(t *testing.T) {
	// 数组形状（多个 ssh 进程）
	arr := []byte(`[{"Id":1287,"CommandLine":"ssh.exe -N -L 3081:127.0.0.1:3080 x1c","StartTime":"2026-10-15T12:00:00Z"},` +
		`{"Id":1288,"CommandLine":"ssh.exe -N -L 9000:127.0.0.1:8080 y","StartTime":"2026-10-15T12:01:00Z"}]`)
	list, err := parseProcsJSON(arr)
	if err != nil || len(list) != 2 {
		t.Fatalf("数组形状解析失败：%v，得到 %d 条", err, len(list))
	}

	// 单对象形状：只有一个 ssh 进程时 ConvertTo-Json 未必套数组
	one := []byte(`{"Id":7,"CommandLine":"ssh.exe -N -L 3081:127.0.0.1:3080 x1c","StartTime":null}`)
	list, err = parseProcsJSON(one)
	if err != nil || len(list) != 1 || list[0].ID != 7 {
		t.Fatalf("单对象形状解析失败：%v，%+v", err, list)
	}

	// 空数组 / null：都是「没有 ssh 进程」，不是错误
	for _, body := range []string{`[]`, `null`, ``, "  \n"} {
		list, err = parseProcsJSON([]byte(body))
		if err != nil || len(list) != 0 {
			t.Errorf("空结果 %q 应得到 0 条且不报错，实际 %v / %d 条", body, err, len(list))
		}
	}
}

func TestParseProcsJSONBadInput(t *testing.T) {
	if _, err := parseProcsJSON([]byte("not json")); err == nil {
		t.Errorf("非法 JSON 应当报错")
	}
}

func TestBuildRows(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 30, 0, 0, time.UTC)
	cmd := `C:\Windows\System32\OpenSSH\ssh.exe -N -L 3081:127.0.0.1:3080 x1c`
	start := "2026-10-15T12:00:00Z"
	rows := buildRows([]winProc{
		{ID: 1287, CommandLine: &cmd, StartTime: &start},
		{ID: 9999, CommandLine: nil, StartTime: nil},
	}, now)

	if len(rows) != 2 {
		t.Fatalf("应产出 2 行，实际 %d 行", len(rows))
	}
	if rows[0].PID != 1287 || rows[0].Spec != "3081:127.0.0.1:3080" || rows[0].Dir != "L" || rows[0].Host != "x1c" {
		t.Errorf("第 1 行错误：%+v", rows[0])
	}
	if rows[0].Etime != "00:30:00" {
		t.Errorf("持续时间 = %q，期望 00:30:00", rows[0].Etime)
	}
	// 命令行拿不到（权限受限等）→ 该段记 "-"，但这一行不能消失（0.01 §4.3 同款原则）
	if rows[1].PID != 9999 || rows[1].Spec != "" || rows[1].Host != "" || rows[1].Etime != "-" {
		t.Errorf("第 2 行（无命令行）错误：%+v", rows[1])
	}
}

func TestSplitCmdline(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`C:\Windows\System32\OpenSSH\ssh.exe -N -L 3081:127.0.0.1:3080 x1c`,
			[]string{`C:\Windows\System32\OpenSSH\ssh.exe`, "-N", "-L", "3081:127.0.0.1:3080", "x1c"}},
		{`"C:\Program Files\OpenSSH\ssh.exe" -N -L 3081:127.0.0.1:3080 x1c`,
			[]string{`C:\Program Files\OpenSSH\ssh.exe`, "-N", "-L", "3081:127.0.0.1:3080", "x1c"}},
		{`  `, nil},
		{`ssh.exe -N   -R 3080:127.0.0.1:3081  y`, []string{"ssh.exe", "-N", "-R", "3080:127.0.0.1:3081", "y"}},
	}
	for _, c := range cases {
		got := splitCmdline(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitCmdline(%q) = %v，期望 %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCmdline(%q)[%d] = %q，期望 %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestElapsedSince(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 30, 0, 0, time.UTC)
	s := "2026-10-15T12:00:00Z"
	if got := elapsedSince(&s, now); got != "00:30:00" {
		t.Errorf("= %q，期望 00:30:00", got)
	}
	if got := elapsedSince(nil, now); got != "-" {
		t.Errorf("缺启动时间应记 -，实际 %q", got)
	}
	bad := "不是时间"
	if got := elapsedSince(&bad, now); got != "-" {
		t.Errorf("解析不了应记 -，实际 %q", got)
	}
}
