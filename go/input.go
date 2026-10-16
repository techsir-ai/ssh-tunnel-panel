// 输入层（TUI）—— 所有「按键 / 逐行输入」只走这一个入口。
//
// 这里的每一条设计都是实机踩出来的，不是想当然：
//
//  1. **行尾到底是 '\r' 还是 '\n' 取决于终端**：Windows 控制台在行输入模式下送 CRLF，
//     在单键模式下只送 '\r'；有的终端（Git Bash / mintty 之类）也只用 '\r'。
//     所以 '\r' 与 '\n' 都算行尾，并把紧随其后的另一个换行符吞掉。
//     —— 之前只认 '\n'，就会出现「敲了 1 没反应」。
//  2. **单键模式下终端不回显**：不回显就得本程序回显，否则用户敲了键屏幕上毫无动静，
//     看起来就像程序死了。
//  3. **选项（1/2）单键即生效，不必回车**；直接回车 = 取默认值。
//  4. **输入非法不中止流程**：当场重问，并把「程序实际收到什么」打出来。
//     中文输入法会给全角字符——肉眼看着是 "1"，程序收到的是 "１"，这类问题不打印出来查不到。
//  5. **期望 ASCII 的字段做全角→半角归一化**（方向 / 编号 / IP / 端口 / 登录）。
//  6. **动作开始前丢弃残留按键**：菜单上那一下回车可能残留 CR/LF，
//     不排掉会「自动」替用户答掉第一个提问。
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------- 读键底层：唯一一个读 stdin 的协程 + 一个回推槽 ----------

var (
	keyCh     = make(chan byte, 64)
	keyReader sync.Once
	pushback  []byte // 多读到的字节回推（吞 CRLF 用）
)

// startKeyReader 起**唯一**一个读 stdin 的协程，只起一次。
// 不能每次读键都开一个协程：那样每个超时周期都会留下一个永久阻塞在 Read 上的协程。
func startKeyReader() {
	keyReader.Do(func() {
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil || n == 0 {
					close(keyCh) // stdin 关了（终端消失 / 管道结束）
					return
				}
				keyCh <- buf[0]
			}
		}()
	})
}

// readKey 读一个字节：超时没键 ok=false；stdin 已关 closed=true。
func readKey(timeout time.Duration) (b byte, ok bool, closed bool) {
	if len(pushback) > 0 {
		b, pushback = pushback[0], pushback[1:]
		return b, true, false
	}
	startKeyReader()
	select {
	case v, more := <-keyCh:
		if !more {
			return 0, false, true
		}
		return v, true, false
	case <-time.After(timeout):
		return 0, false, false
	}
}

// waitKey 阻塞读一个字节（交互场景：等到有键为止；stdin 关闭则 closed=true）。
func waitKey() (b byte, closed bool) {
	for {
		v, ok, closed := readKey(24 * time.Hour)
		if closed {
			return 0, true
		}
		if ok {
			return v, false
		}
	}
}

// skipLeadingNewlines 丢掉紧随其后的**一个**换行（2 毫秒窗口内）。
//
// 用在菜单键之后：无论交互还是管道，"n 后面那个换行"都是菜单键这一行的行尾，
// 不该被当成下一个提问的答案（否则会"自动"把第一问答成空值）。
// 只丢一个：多丢会把下一问的空答案也吃掉。
func skipLeadingNewlines() { swallowNewlines(1) }

// cleanupStaleNewlines 清掉**交互**时用户按惯了的回车。
//
// 只在终端交互时清：管道 / 重定向输入是脚本化的应答序列，
// 空行本身就是要紧的答案（例如「远程端口留空 = 取本地端口」），清掉就会错位。
func cleanupStaleNewlines() {
	if isTerminal() {
		swallowNewlines(2)
	}
}

// ---------- 回显：终端不回显时由本程序代劳 ----------

func echoByte(b byte) {
	if needSelfEcho() {
		_, _ = os.Stdout.Write([]byte{b})
	}
}

func echoStr(s string) {
	if needSelfEcho() {
		_, _ = os.Stdout.WriteString(s)
	}
}

func echoNewline() { echoStr("\n") }

// peekKey 看一眼队头的字节但**不消费**它。
// 不能"取出再放回"——那会把字节挪到队尾，打乱输入顺序（实机上整段输入都会错位）。
func peekKey(timeout time.Duration) (b byte, ok bool) {
	if len(pushback) > 0 {
		return pushback[0], true
	}
	startKeyReader()
	select {
	case v, more := <-keyCh:
		if !more {
			return 0, false
		}
		pushback = append(pushback, v) // 取出来立刻放回队头；此时 pushback 为空，顺序不变
		return v, true
	case <-time.After(timeout):
		return 0, false
	}
}

// swallowNewlines 吞掉紧随其后的换行符（最多 n 个）。
//   - 行尾之后再吞一个：CRLF 的另一半（'\r' 之后往往还跟着 '\n'）。
//   - 单键选项之后再吞：用户按惯了回车，那个回车若留在队列里，
//     会"自动"把下一个提问答成空值（这是实机上出现过的坑）。
//
// 只看不动：队头不是换行就立刻收手，绝不改动队列次序。
func swallowNewlines(n int) {
	for i := 0; i < n; i++ {
		b, ok := peekKey(2 * time.Millisecond)
		if !ok {
			return
		}
		if b != '\r' && b != '\n' {
			return
		}
		if len(pushback) > 0 {
			pushback = pushback[1:] // 丢掉队头那个换行
		}
	}
}

// completeRune 若 b 是 UTF-8 首字节，补读后续字节（终端通常一次给全，故超时很短）。
func completeRune(b byte) string {
	out := []byte{b}
	if b < 0xC0 {
		return string(out)
	}
	need := 1 // 2 字节字符
	switch {
	case b >= 0xF0:
		need = 3 // 4 字节字符
	case b >= 0xE0:
		need = 2 // 3 字节字符（全角字符、汉字都在这里）
	}
	for i := 0; i < need; i++ {
		nb, ok := peekKey(30 * time.Millisecond)
		if !ok {
			break
		}
		if nb < 0x80 || nb > 0xBF {
			break // 不是后续字节：留着不动（绝不改动队列次序）
		}
		out = append(out, nb)
		pushback = pushback[1:] // 消费掉
		echoByte(nb)
	}
	return string(out)
}

// ---------- 三个提问原语 ----------

// readLineInput 读一行（单键模式下的自建行编辑：回显 + 退格）。
// eof=true 表示 stdin 已关闭且这一行没有任何内容。
func readLineInput() (line string, eof bool) {
	var out []byte
	for {
		b, closed := waitKey()
		if closed {
			if len(out) == 0 {
				return "", true
			}
			return string(out), false
		}
		switch b {
		case '\r', '\n':
			// 到此为止：不再多吞一个换行。
			// 单键模式下回车只给 '\r'，不存在 CRLF 配对问题；
			// 多吞一个会把下一问「空行 = 取默认」这个有意义的答案吃掉（管道输入实测踩到）。
			echoNewline()
			return string(out), false
		case 0x08, 0x7f: // 退格 / Delete
			if n, w := lastRuneWidth(out); n > 0 {
				out = out[:len(out)-n]
				for i := 0; i < w; i++ {
					echoStr("\b \b")
				}
			}
		case 0x03: // Ctrl+C：放弃本行
			echoStr("^C")
			echoNewline()
			return "", false
		default:
			out = append(out, b)
			echoByte(b)
		}
	}
}

// readChoice 读一个选项键：**单键即生效，不必回车**；回车 = 空答案（取默认）。
func readChoice() (ans string, eof bool) {
	b, closed := waitKey()
	if closed {
		return "", true
	}
	switch b {
	case '\r', '\n':
		swallowNewlines(1)
		echoNewline()
		return "", false
	case 0x03:
		echoStr("^C")
		echoNewline()
		return "", false
	}
	echoByte(b)
	s := completeRune(b)
	echoNewline()
	swallowNewlines(1) // 这一行的行尾（用户习惯性敲的回车）：吃掉，别让它替下一问作答
	return s, false
}

// askPrompt 打印提示并读一行，只去首尾空白（中文原样保留）。
func askPrompt(prompt string) (string, bool) {
	cleanupStaleNewlines() // 交互时先排掉上一问残留的回车；管道输入不动（空行是有意义的答案）
	fmt.Print(prompt)
	line, eof := readLineInput()
	if eof {
		return "", true
	}
	return strings.TrimSpace(line), false
}

// askPromptASCII 同上，但把全角折成 ASCII（IP / 端口 / 登录这类字段只该是 ASCII）。
// 发生转换时明确提示，避免「屏幕上看着是 1、程序里却是别的」。
func askPromptASCII(prompt string) (string, bool) {
	s, eof := askPrompt(prompt)
	if eof {
		return "", true
	}
	n := normalizeASCII(s)
	if n != s {
		fmt.Printf("    （已按 ASCII 处理：「%s」→「%s」）\n", s, n)
	}
	return n, false
}

// askChoice 读一个选项：opts 里列出所有合法答案（"" 表示允许回车取默认）。
// 非法输入**不退出流程**，当场重问并把实际收到的内容打出来。
func askChoice(prompt string, opts ...string) (string, bool) {
	for {
		cleanupStaleNewlines() // 先排掉残留换行，再打提示
		fmt.Printf("    > ")
		raw, eof := readChoice()
		if eof {
			return "", true
		}
		v := normalizeASCII(raw)
		for _, o := range opts {
			if v == o {
				if v != raw && raw != "" {
					// 输入法给的是全角：程序按半角处理，明确说一声
					fmt.Printf("    （已按 ASCII 处理：「%s」→「%s」）\n", raw, v)
				}
				return v, false
			}
		}
		var names []string
		for _, o := range opts {
			if o != "" {
				names = append(names, o)
			}
		}
		fmt.Printf("    输入无效：程序收到的是「%s」，本项接受 %s（回车 = 取默认）。\n",
			describeInput(raw), strings.Join(names, " / "))
		fmt.Printf("    %s\n", prompt)
	}
}

// askPort 提问一个 1-65535 的端口；allowEmpty=true 时空输入合法（用于「留空取本地端口」）。
func askPort(head, prompt string, allowEmpty bool) (string, bool) {
	for {
		fmt.Print(head)
		s, eof := askPromptASCII(prompt)
		if eof {
			return "", true
		}
		if s == "" && allowEmpty {
			return "", false
		}
		if isPort(s) {
			return s, false
		}
		fmt.Printf("    端口必须是 1-65535 的数字（收到「%s」）。\n", describeInput(s))
	}
}

// askInt 提问一个编号。ok=false 表示用户直接回车取消；eof=true 表示输入源结束。
func askInt(prompt string) (n int, ok bool, eof bool) {
	for {
		s, e := askPrompt(prompt)
		if e {
			return 0, false, true
		}
		if s == "" {
			return 0, false, false // 回车 = 取消
		}
		if v, err := strconv.Atoi(normalizeASCII(s)); err == nil {
			return v, true, false
		}
		fmt.Printf("    编号必须是数字（收到「%s」）。\n", describeInput(s))
	}
}

// ---------- 归一化与回显显示 ----------

// normalizeASCII 全角→半角（中文输入法常见），并丢掉空白与全角空格。
func normalizeASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			b.WriteRune(r - 0xFEE0)
		case r == 0x3000 || r == ' ' || r == '\t':
			// 丢掉
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// describeInput 把收到的内容转成「看得见」的形式：
// 非法 UTF-8 按字节十六进制（GBK 输入法常见），控制字符转义。
// 只有这样才看得出输入法 / 终端到底送来了什么。
func describeInput(s string) string {
	if s == "" {
		return "空"
	}
	var b strings.Builder
	if utf8.ValidString(s) {
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, "\\x%02X", r)
			} else {
				b.WriteRune(r)
			}
		}
	} else {
		for i := 0; i < len(s); i++ {
			fmt.Fprintf(&b, "\\x%02X", s[i])
		}
	}
	return b.String()
}

// lastRuneWidth 返回末尾字符的字节数与显示宽度（退格时按宽度擦除）。
func lastRuneWidth(b []byte) (n, width int) {
	r, size := utf8.DecodeLastRune(b)
	if size <= 0 {
		return 0, 0
	}
	if r == utf8.RuneError && size == 1 {
		return 1, 1 // 非法字节：按一个普通字符处理
	}
	return size, runeWidth(r)
}

// runeWidth 估算字符占用几列（只用于退格擦除，覆盖 CJK 常见区间即可）。
func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}
