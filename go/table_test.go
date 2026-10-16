// 表格渲染的单测：内容按显示宽度补齐后，`|` 必须上下对齐。
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispWidthCountsCJKAsTwo(t *testing.T) {
	cases := []struct{ in string; want int }{
		{"", 0},
		{"abc", 3},
		{"说明", 4},           // 2 个字符，占 4 列
		{"本地ip:端口", 11},      // 4 + 2 + 1 + 4
		{"本程序", 6},
		{"127.0.0.1:3081", 14},
	}
	for _, c := range cases {
		if got := dispWidth(c.in); got != c.want {
			t.Errorf("dispWidth(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

func TestPadCell(t *testing.T) {
	if got := padCell("1", 4, alignLeft); got != "1   " {
		t.Errorf("左对齐：%q", got)
	}
	if got := padCell("1", 4, alignRight); got != "   1" {
		t.Errorf("右对齐：%q", got)
	}
	if got := padCell("本程序", 6, alignLeft); got != "本程序" {
		t.Errorf("刚好同宽不该补：%q", got)
	}
	// 超出宽度宁可这一列歪，也不截断内容
	if got := padCell("127.0.0.1:3081", 8, alignLeft); got != "127.0.0.1:3081" {
		t.Errorf("超宽应原样输出：%q", got)
	}
}

// 逐字节比对整张状态表的输出：每列的 | 必须对齐、最后一列不补空格。
func TestWriteTableStatusShape(t *testing.T) {
	rows := [][]string{
		{"1", "L", "127.0.0.1:3081", "127.0.0.1:3080", "mac", "00:13:20", "-", "本程序", "21696", "01"},
		{"2", "R", "127.0.0.1:3082", "10.0.0.5:8080", "changwei@x1c", "01:02:03", "79.9 KiB", "外部", "999", "从x1c的3080到本机3081"},
	}
	// 期望值按「列宽 + " | " 分隔符」写死（分隔符自带前后各一个空格，别漏了前导那个）
	want := "" +
		"编号 | 方向 | 本地ip:端口    | 转发目标       | SSH主机      | 持续时间 | 通讯量   | 来源   |   PID | 说明\n" +
		"   1 | L    | 127.0.0.1:3081 | 127.0.0.1:3080 | mac          | 00:13:20 | -        | 本程序 | 21696 | 01\n" +
		"   2 | R    | 127.0.0.1:3082 | 10.0.0.5:8080  | changwei@x1c | 01:02:03 | 79.9 KiB | 外部   |   999 | 从x1c的3080到本机3081\n"

	var buf bytes.Buffer
	writeTable(&buf, statusHead, rows, statusAligns)
	if got := buf.String(); got != want {
		// 空格换成一个点，肉眼才能数清列宽
		t.Errorf("状态表输出不符。\n实际:\n%s\n期望:\n%s",
			strings.ReplaceAll(got, " ", "·"), strings.ReplaceAll(want, " ", "·"))
	}
}

// 每行的 | 位置必须完全一致（这是"表格对齐"的硬指标，不依赖手写期望值）。
func TestWriteTablePipesAlign(t *testing.T) {
	rows := [][]string{
		{"1", "L", "127.0.0.1:3081", "127.0.0.1:3080", "mac", "00:13:20", "-", "本程序", "21696", "01"},
		{"12", "R", "127.0.0.1:3082", "10.0.0.5:8080", "changwei@x1c", "01:02:03", "1.2 MiB", "外部", "9", "-"},
	}
	var buf bytes.Buffer
	writeTable(&buf, statusHead, rows, statusAligns)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("行数不对：%d", len(lines))
	}
	// 以表头的 | 位置为基准：所有分隔符的显示列位置都得一样
	base := pipePositions(lines[0])
	if len(base) == 0 {
		t.Fatal("表头没有分隔符？")
	}
	for i, ln := range lines[1:] {
		if got := pipePositions(ln); !equalInts(got, base) {
			t.Errorf("第 %d 行的分隔符位置 %v 与表头 %v 不一致：\n%q\n%q", i+1, got, base, lines[0], ln)
		}
	}
	// 最后一列不补空格：行尾不能是空格
	for i, ln := range lines {
		if strings.HasSuffix(ln, " ") {
			t.Errorf("第 %d 行尾部有空格：%q", i, ln)
		}
	}
}

// 配置表：说明不是最后一列，所以它也必须补齐（状态列才会对齐）。
func TestWriteTableConfigShape(t *testing.T) {
	rows := [][]string{
		{"1", "L", "127.0.0.1:3081", "127.0.0.1:3080", "mac", "01", "未运行"},
		{"2", "R", "127.0.0.1:3082", "10.0.0.5:8080", "changwei@x1c", "从x1c过来", "已运行 pid 21696"},
	}
	var buf bytes.Buffer
	writeTable(&buf, configHead, rows, configAligns)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	base := pipePositions(lines[0])
	for i, ln := range lines[1:] {
		if got := pipePositions(ln); !equalInts(got, base) {
			t.Errorf("配置表第 %d 行未对齐：\n%q\n%q", i+1, lines[0], ln)
		}
	}
	if !strings.Contains(buf.String(), "已运行 pid 21696") {
		t.Error("状态列内容丢了")
	}
}

// pipePositions 返回该行所有 '|' 的**显示列位置**（中文按两列算）。
func pipePositions(line string) []int {
	var res []int
	col := 0
	for _, r := range line {
		if r == '|' {
			res = append(res, col)
		}
		col += runeWidth(r)
	}
	return res
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
