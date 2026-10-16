// 通讯量列的单测：解析 nettop 的 CSV 与字节数格式化。
package main

import "testing"

// 真实样例（macOS 上 `nettop -P -x -n -L 1 -p ssh` 的原始输出）。
func TestParseNettopRealSample(t *testing.T) {
	sample := `time,,interface,state,bytes_in,bytes_out,rx_dupe,rx_ooo,re-tx,rtt_avg,rcvsize,tx_win,tc_class,tc_mgt,cc_algo,P,C,R,W,arch,
11:38:09.795234,ssh.91215,,,4283426,4201415,10920,1200,2840,,,,,,,,,,,,
`
	got := parseNettop([]byte(sample))
	if len(got) != 1 {
		t.Fatalf("应解析出 1 条，实际 %d 条：%v", len(got), got)
	}
	// 口径：收发合计 ÷ 2
	if want := int64((4283426 + 4201415) / 2); got[91215] != want {
		t.Errorf("pid 91215 应为 %d，实际 %d", want, got[91215])
	}
}

// 表头、字段不齐、pid 不是数字、字节字段不是数字、负数 —— 一律跳过，不许报错也不许写脏值。
func TestParseNettopSkipsBadLines(t *testing.T) {
	sample := "header,row,with,enough,fields,x\n" +
		"bad,line\n" +
		"time,ssh.abc,,,1,2\n" + // pid 非数字
		"time,ssh.,,,1,2\n" + // 点号后为空
		"time,sshd,,,1,2\n" + // 没有 name.pid 形式
		"time,ssh.0,,,1,2\n" + // pid 为 0
		"time,ssh.123,,,notanum,2\n" + // bytes_in 非数字
		"time,ssh.124,,,-1,2\n" + // 负数
		",,,,,\n" +
		"time,ssh.125,,,1024,1024\n" // 唯一合法的一条

	got := parseNettop([]byte(sample))
	if len(got) != 1 {
		t.Fatalf("只应留下 1 条，实际 %d 条：%v", len(got), got)
	}
	if got[125] != 1024 {
		t.Errorf("pid 125 应为 1024，实际 %d", got[125])
	}
}

// 真实样例（pve 上 `ss -tinp` 的原始输出节选）。
func TestParseSSRealSample(t *testing.T) {
	sample := "ESTAB 0 0 192.168.8.100:22 192.168.8.10:7069 users:((\"sshd-session\",pid=1630310,fd=7),(\"sshd-session\",pid=1630277,fd=7))\n" +
		"\t cubic wscale:8,10 rto:240 rtt:39.275/12.196 ato:40 mss:1240 pmtu:1500 rcvmss:1240 advmss:1460 cwnd:10 bytes_sent:4325 bytes_acked:4325 bytes_received:4006 segs_out:21 segs_in:17\n" +
		"ESTAB 0 0 192.168.8.100:54388 87.210.193.150:22067 users:((\"syncthing\",pid=1062,fd=9))\n" +
		"\t cubic wscale:7,10 rto:419 rtt:218.24/0.263 mss:1440 bytes_sent:63682 bytes_retrans:952 bytes_acked:62731 bytes_received:62611 segs_out:12433\n"
	got := parseSS([]byte(sample))
	if len(got) != 3 {
		t.Fatalf("应解析出 3 个 pid，实际 %d 个：%v", len(got), got)
	}
	if got[1630310] != 4325+4006 || got[1630277] != 4325+4006 {
		t.Errorf("共享 fd 的两个 pid 各应记一份：%v", got)
	}
	if got[1062] != 63682+62611 {
		t.Errorf("发送侧取 bytes_sent 与 bytes_acked 的较大者，实际 %v", got)
	}
}

// 新内核已废弃 bytes_sent（会读成 0）→ 必须用 bytes_acked 兜底。
func TestParseSSBytesSentDeprecated(t *testing.T) {
	sample := "ESTAB 0 0 1.2.3.4:22 5.6.7.8:1234 users:((\"ssh\",pid=100,fd=3))\n" +
		"\t cubic bytes_sent:0 bytes_acked:1000 bytes_received:500\n"
	got := parseSS([]byte(sample))
	if got[100] != 1500 {
		t.Errorf("pid 100 应为 1500，实际 %d", got[100])
	}
}

// 没有 pid 或没有字节字段的行一律不产生条目（拿不到就记 -，不许编）。
func TestParseSSSkipsUseless(t *testing.T) {
	sample := "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n" +
		"\t cubic wscale:8,10\n" +
		"ESTAB 0 0 1.2.3.4:22 5.6.7.8:1234\n" +
		"\t cubic bytes_sent:10 bytes_received:5\n"
	if got := parseSS([]byte(sample)); len(got) != 0 {
		t.Errorf("不该产生条目，实际 %v", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"}, // 不足 1 KiB：整数 + B（与 bash 版 human_bytes 一致）
		{1024, "1.0 KiB"},
		{81817, "79.9 KiB"},
		{1048576, "1.0 MiB"},
		{4242420, "4.0 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
