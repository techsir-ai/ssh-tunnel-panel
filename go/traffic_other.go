//go:build !darwin && !linux

// 通讯量取数（Windows）：**没有免权限的等价数据源**。
//
// 结论来自实测与微软文档（2026-10-16 在老板机器上验过）：
//   - Windows 的 TCP 栈**确实有**每连接字节计数（WFP 的 TCP 扩展统计 TCP_ESTATS_DATA），
//     但它**默认关闭**，必须先用 SetPerTcpConnectionEStats 打开，而那个调用要求
//     **「管理员组成员」且「进程已提升」**——两者缺一不可：UAC 开着时，从普通终端启动的
//     进程拿到的是过滤令牌，令牌里 Administrators 组被标成「只用于拒绝的组」，
//     于是同一个账号也判为无权限（实测返回 ERROR_ACCESS_DENIED）。
//     文档还警告：Set 失败时 Get 会返回无意义的随机数据 —— 所以不能"读不到就算"。
//   - 进程级「全部 IO 字节」不等于网络字节，拿它冒充这一列会误导。
//
// 所以非提升权限的 Windows 上这一列恒为 `-`；要在 Windows 上有值，得让 sshtp 以管理员运行
//（那属于需求变更，未擅自实现）。
//
// Linux 有等价数据源，走 traffic_linux.go。
package main

func trafficMap() map[int]string { return nil }
