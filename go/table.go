// 表格渲染 —— 让每列的 `|` 上下对齐。
//
// 规则（**两个实现共用**，bash 版回填时照做）：
//
//  1. 每列宽度 = 「表头显示宽度」与「该列所有单元格显示宽度」的最大值；
//  2. 宽度按**显示宽度**算，不是字符数也不是字节数 —— 中文占两列，
//     拿 `len()` 去补必然歪（"说明"是 2 个字符但占 4 列）；
//  3. 纯数字列（编号 / PID）右对齐，其余左对齐；
//  4. **最后一列不补空格**（后面没有东西需要它对齐），否则每行尾部拖一串看不见的空格；
//  5. 列间分隔符固定 ` | `。
//
// 为什么要按「表头 + 实际内容」现算宽度：状态表每 5 秒重绘一次，内容随时在变，
// 写死宽度必然被更长的内容挤歪；现算则永远对齐。
package main

import (
	"io"
	"os"
	"strings"
)

type alignKind int

const (
	alignLeft alignKind = iota
	alignRight
)

// dispWidth 字符串的显示宽度：中文等宽字符按两列算。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// padCell 补空格到 w 宽；已超出则原样返回（宁可这一列歪，也不截断内容）。
func padCell(s string, w int, a alignKind) string {
	need := w - dispWidth(s)
	if need <= 0 {
		return s
	}
	sp := strings.Repeat(" ", need)
	if a == alignRight {
		return sp + s
	}
	return s + sp
}

// writeTable 输出一张无边框表格（` | ` 分隔）。
// head 与每行单元格数应一致；cols 缺项按左对齐处理。
func writeTable(w io.Writer, head []string, rows [][]string, cols []alignKind) {
	widths := make([]int, len(head))
	for i, h := range head {
		widths[i] = dispWidth(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && dispWidth(c) > widths[i] {
				widths[i] = dispWidth(c)
			}
		}
	}

	alignOf := func(i int) alignKind {
		if i < len(cols) {
			return cols[i]
		}
		return alignLeft
	}

	row := func(cells []string) {
		var b strings.Builder
		for i, c := range cells {
			if i > 0 {
				b.WriteString(" | ")
			}
			if i == len(cells)-1 {
				b.WriteString(c) // 最后一列不补空格
				break
			}
			if i < len(widths) {
				b.WriteString(padCell(c, widths[i], alignOf(i)))
			} else {
				b.WriteString(c)
			}
		}
		b.WriteByte('\n')
		_, _ = io.WriteString(w, b.String())
	}

	row(head)
	for _, r := range rows {
		row(r)
	}
}

func printTable(head []string, rows [][]string, cols []alignKind) {
	writeTable(os.Stdout, head, rows, cols)
}

// 状态表 / 配置表的列定义（宽度现算，这里的对齐方式是固定约定）。
var (
	statusHead   = []string{"编号", "方向", "本地ip:端口", "转发目标", "SSH主机", "持续时间", "通讯量", "来源", "PID", "说明"}
	statusAligns = []alignKind{alignRight, alignLeft, alignLeft, alignLeft, alignLeft, alignLeft, alignLeft, alignLeft, alignRight, alignLeft}

	configHead   = []string{"编号", "方向", "本地ip:端口", "转发目标", "SSH主机", "说明", "状态"}
	configAligns = []alignKind{alignRight, alignLeft, alignLeft, alignLeft, alignLeft, alignLeft, alignLeft}
)
