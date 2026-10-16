//go:build !darwin

// 通讯量取数（Linux / Windows）：**没有等价数据源**。
//
// 0.01 §8 已写明这是 macOS 专属能力：
//   - Linux 没有进程级网络字节计数（要 nethogs / eBPF，等于新增依赖）；
//   - Windows 只有 IO 计数器近似，不是同一回事。
//
// 所以这两个平台这一列恒为 `-` —— 是需求定的边界，不是漏实现。
package main

func trafficMap() map[int]string { return nil }
