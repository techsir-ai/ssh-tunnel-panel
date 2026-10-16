// 输入层的单元测试。
//
// 这一层的 bug 全在「行尾是 '\r' 还是 '\n'」「终端有没有回显」「输入法给的是全角」
// 这类边界上，所以用例就照着这些边界写：全部通过 pushback 直接喂字节，
// 不碰真实终端（真终端的部分用另一套探针在实机上跑）。
package main

import "testing"

// feed 把字节放进回推槽，后续读取先吃这里。
func feed(s string) {
	pushback = append([]byte{}, []byte(s)...)
}

func drainPushback() []byte {
	b := pushback
	pushback = nil
	return b
}

func TestNormalizeASCII(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1", "1"},
		{"１", "1"},   // 全角数字：中文输入法最常见的坑
		{"２", "2"},
		{"ｎ", "n"},   // 全角字母
		{"１２７.０.０.１：３０８０", "127.0.0.1:3080"}, // 全角 IP:端口
		{" 3081 ", "3081"},
		{"chángwěi", "chángwěi"}, // 拉丁扩展字母：保留
		{"测试隧道", "测试隧道"},       // 中文：原样保留
	}
	for _, c := range cases {
		if got := normalizeASCII(c.in); got != c.want {
			t.Errorf("normalizeASCII(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestDescribeInput(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "空"},
		{"1", "1"},
		{"１２", "１２"},         // 合法 UTF-8：正常显示
		{"\xa3\xb1", "\\xA3\\xB1"}, // GBK 的全角「１」：不是合法 UTF-8，按字节打出
		{"\x01", "\\x01"},
	}
	for _, c := range cases {
		if got := describeInput(c.in); got != c.want {
			t.Errorf("describeInput(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 行尾三种形态都必须能结束一行：LF、CR（单键模式下回车只给 CR）、CRLF。
// CRLF 的第二个字符会留在队列里 —— 这是刻意的：不再多吞，
// 否则会把下一问「空行 = 取默认」这个有意义的答案吃掉。残留由提问前的清理负责。
func TestReadLineAcceptsCRAndLFAndCRLF(t *testing.T) {
	cases := []struct{ in, want, left string }{
		{"3081\n", "3081", ""},
		{"3081\r", "3081", ""},
		{"3081\r\n", "3081", "\n"},
	}
	for _, c := range cases {
		feed(c.in)
		got, eof := readLineInput()
		if eof {
			t.Fatalf("输入 %q：不应报告 eof", c.in)
		}
		if got != c.want {
			t.Errorf("输入 %q：读到 %q，期望 %q", c.in, got, c.want)
		}
		if left := string(drainPushback()); left != c.left {
			t.Errorf("输入 %q：回推槽应为 %q，实际 %q", c.in, c.left, left)
		}
	}
}

// 选项单键即生效，不必回车。
func TestReadChoiceSingleKeyWithoutEnter(t *testing.T) {
	feed("1")
	got, eof := readChoice()
	if eof || got != "1" {
		t.Fatalf("单键读取失败：got=%q eof=%v", got, eof)
	}
}

// 用户按惯了回车：单键选项之后的那个行尾必须被吃掉，
// 否则它会替下一个提问答成空值（实机上出现过的坑）。
func TestReadChoiceSwallowsHabitualEnter(t *testing.T) {
	feed("1\r")
	got, _ := readChoice()
	if got != "1" {
		t.Fatalf("got=%q，期望 1", got)
	}
	if left := drainPushback(); len(left) != 0 {
		t.Errorf("回推槽残留 %q（回车没被吃掉）", left)
	}
}

// 空行（直接回车）= 取默认，返回空串。
func TestReadChoiceEmptyMeansDefault(t *testing.T) {
	feed("\r\n")
	got, eof := readChoice()
	if eof || got != "" {
		t.Fatalf("回车应返回空串，实际 got=%q eof=%v", got, eof)
	}
	if left := drainPushback(); len(left) != 0 {
		t.Errorf("回推槽残留 %q", left)
	}
}

// 非法输入（全角数字）必须当场重问，而不是把流程做死。
func TestAskChoiceReasksOnFullWidthThenAccepts(t *testing.T) {
	feed("９2") // 全角 ９（非法）→ 半角 2（合法）
	got, eof := askChoice("[1]/[2]", "1", "2")
	if eof {
		t.Fatal("不应 eof")
	}
	if got != "2" {
		t.Fatalf("重问后应读到 2，实际 %q", got)
	}
}

// 菜单键归一化：大写、全角都要能认。
func TestNormKey(t *testing.T) {
	if got := normKey('N'); got != 'n' {
		t.Errorf("normKey('N') = %q，期望 n", got)
	}
	if got := normKey('q'); got != 'q' {
		t.Errorf("normKey('q') = %q，期望 q", got)
	}

	// 全角 ｎ：首字节交给 normKey，后续字节留在队列里（真实终端就是这样一次给全）
	fw := []byte("ｎ")
	feed(string(fw[1:]))
	if got := normKey(fw[0]); got != 'n' {
		t.Errorf("全角 ｎ 应归一到 n，实际 %q", got)
	}

	// 汉字：不该被误认成任何命令
	hz := []byte("中")
	feed(string(hz[1:]))
	if got := normKey(hz[0]); got != 0 {
		t.Errorf("汉字不该匹配任何按键，实际 %q", got)
	}
	drainPushback()
}

// 菜单键之后只丢**一个**开头的换行，其余按键原样保留、次序不变。
// （踩过的坑：原先"取出再放回"会把字节挪到队尾，整段输入就错位了。）
func TestSkipLeadingNewlinesDropsOnlyOne(t *testing.T) {
	feed("\n\n1\r\n2")
	skipLeadingNewlines()
	if got := string(drainPushback()); got != "\n1\r\n2" {
		t.Errorf("只该丢掉开头一个换行、且次序不变，实际 %q", got)
	}
}

// 队头不是换行时，什么都不许动。
func TestSkipLeadingNewlinesLeavesDataIntact(t *testing.T) {
	feed("1\r\n")
	skipLeadingNewlines()
	if got := string(drainPushback()); got != "1\r\n" {
		t.Errorf("队头不是换行时不该改动队列，实际 %q", got)
	}
}

// 退格：ASCII 一个字节，汉字三个字节但要按两列擦除。
func TestLastRuneWidth(t *testing.T) {
	n, w := lastRuneWidth([]byte("abc"))
	if n != 1 || w != 1 {
		t.Errorf("ascii: n=%d w=%d，期望 1/1", n, w)
	}
	n, w = lastRuneWidth([]byte("隧道"))
	if n != 3 || w != 2 {
		t.Errorf("汉字: n=%d w=%d，期望 3/2", n, w)
	}
	n, w = lastRuneWidth([]byte{0xa3}) // 非法字节
	if n != 1 || w != 1 {
		t.Errorf("非法字节: n=%d w=%d，期望 1/1", n, w)
	}
}
