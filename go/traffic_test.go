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
