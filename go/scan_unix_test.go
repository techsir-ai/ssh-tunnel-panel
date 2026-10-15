//go:build !windows

// Unix 侧取数解析的单元测试。
// 用例里的 lsof / ps 输出按真实格式构造（macOS 实机采集过），用于锁定解析行为，
// 这样在开发机（Windows，没有 lsof）上也能验证这一路是否忠实。
package main

import "testing"

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
  6439 09-14:32:42 /usr/bin/ssh-agent -l
 64822       00:00 sshd-session: changwei [priv]
`
	rows := parsePs(out)
	if len(rows) != 3 {
		t.Fatalf("应解析出 3 条 ssh 进程（ssh-agent / sshd-session 都不是隧道），实际 %d 条：%+v", len(rows), rows)
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
