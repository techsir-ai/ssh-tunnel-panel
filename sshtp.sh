#!/usr/bin/env bash
#
# sshtp —— SSH 本地转发隧道管理器（macOS / Linux）
#
# 需求见同目录「需求文档.md」，配置见同目录「sshtp.cfg」。
#
# 用法（CLI 与 TUI 共用同一套键位）：
#   sshtp           打印状态 + 全部功能/参数/快捷键；3 秒内按任意键进 TUI 菜单，否则退出
#   sshtp v         查看配置
#   sshtp n <说明> <本地ip:端口> <远程ip:端口> <user@host>   新建
#   sshtp e <编号> <字段> <新值>           修改
#   sshtp x <编号>                         删除配置
#   sshtp c <编号>                         连接
#   sshtp d <编号>                         断开
#
# 无参数那一次输出就是完整说明（含 e 的字段名），故不设 h / -h / --help 子命令。
# 分层：core（无任何交互）/ TUI 前端 / CLI 前端。一份 core，两个前端。
#
# 配置文件 sshtp.cfg（与本程序同目录），每行一条隧道，**四列逗号分隔**，顺序固定：
#   <说明>,<localip>:<localport>,<remoteip>:<remoteport>,<remoteuser@remoteip>
# 恰好 4 段；多一段或少一段该行即视为无效（跳过）。说明可含空格，但**不能含逗号**
# （不做引号转义 —— 逗号是分隔符，出现在说明里会打乱列数，落盘时直接拒绝）。
# 拼接：ssh -N -L <列2>:<列3> <列4>
# 列4：带 @ → 显式指定用户名；不带 @ → 整值视为 ~/.ssh/config 里的 Host（别名）
#
# TUI：不按键则每 5 秒自动刷新（状态表含「持续时间 / 通讯量」，需要实时重绘）。
#
# 依赖：bash、ps、lsof、ssh（macOS / Linux 自带）
#       nettop（macOS 自带，可选）—— 取每个 ssh 进程的收发字节，用于状态表的「通讯量」列；
#       没有 nettop（如 Linux）时该列显示 `-`，其余功能不受影响。

# ---------- 定位自身与三个文件（扁平，全在同目录） ----------
# CONFIG / LOG 常驻；PID 文件是运行时临时文件，只在有本程序起的隧道时存在，
# 最后一条断开后自动删除（见 remove_pid）。
case "$0" in
  */*) DIR=$(cd "$(dirname "$0")" && pwd) ;;
  *)   DIR=$(cd "$(dirname "$(command -v "$0")")" && pwd) ;;
esac
CONF="$DIR/sshtp.cfg"
LOGF="$DIR/sshtp.log"
PIDF="$DIR/sshtp.pid"

# TUI 自动刷新间隔（秒）：不按键则每 TICK 秒重绘一次状态
TICK=5

# ==========================================================
#  core —— 无任何交互（不 clear、不 read、不 pause）
# ==========================================================

# 日志一行：<时间>  <动作>  <结果>  <本地> -> <远程>  <登录>  (pid N)
# $1=动作(连接/断开)  $2=结果(成功/失败)  $3=本地ip:端口  $4=远程ip:端口  $5=登录目标  $6=pid
log_result() {
  printf '%s  %s  %s  %s -> %s  %s  (pid %s)\n' \
    "$(date '+%Y-%m-%d %H:%M:%S')" "$1" "$2" "${3:--}" "${4:--}" "${5:--}" "${6:--}" >> "$LOGF"
}

# ---------- 配置读写 ----------

# 有效配置行（跳过空行与 # 注释），输出 "<序号><TAB>原始行"
list_conf() {
  [ -f "$CONF" ] || return 0
  awk 'NF && $1 !~ /^#/ { n++; printf "%d\t%s\n", n, $0 }' "$CONF"
}

conf_count() { list_conf | wc -l | tr -d ' '; }

# 取第 n 条配置的原始行
conf_line() {
  list_conf | awk -F'\t' -v n="$1" '$1==n { print substr($0, length($1)+2); exit }'
}

# 解析一行配置（逗号分隔，必须恰好 4 段）→ 输出：列2<TAB>列3<TAB>列4<TAB>说明
# 段数不对 → 无输出（调用方据此判为无效行）
parse_line() {
  awk -F',' '
    NF != 4 { exit }
    {
      for (i = 1; i <= 4; i++) { gsub(/^[ \t]+/, "", $i); gsub(/[ \t]+$/, "", $i) }
      print $2 "\t" $3 "\t" $4 "\t" $1
    }'
}

# 拆 ip:port（只有端口时 ip 记 127.0.0.1）
# 注意：不能用 `read` 拆 —— 首字段为空时前导 TAB 会被 IFS 吃掉，导致两字段错位
addr_ip()   { case "$1" in *:*) printf '%s\n' "${1%%:*}" ;; *) printf '127.0.0.1\n' ;; esac; }
addr_port() { case "$1" in *:*) printf '%s\n' "${1##*:}" ;; *) printf '%s\n' "$1" ;; esac; }

# 拆 列4（不带 @ 时 user 为空）
c4_user()   { case "$1" in *@*) printf '%s\n' "${1%%@*}" ;; *) printf '\n' ;; esac; }
c4_host()   { case "$1" in *@*) printf '%s\n' "${1#*@}" ;; *) printf '%s\n' "$1" ;; esac; }

# 校验端口：1-65535 的纯数字
is_port() {
  case "$1" in ''|*[!0-9]*) return 1 ;; esac
  [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

# 含逗号？—— 逗号是列分隔符，出现在任何一段里都会打乱列数，落盘一律拒绝
has_comma() { case "$1" in *,*) return 0 ;; *) return 1 ;; esac; }

# 追加一条配置：desc lip lport rip rport c4
# c4 必须由调用方拼好 —— 「带 @ 显式指定用户」或「不带 @ 走 ~/.ssh/config」
add_conf() {
  printf '%s,%s:%s,%s:%s,%s\n' "$1" "$2" "$3" "$4" "$5" "$6" >> "$CONF"
}

# 删除第 n 条配置（其余行原样保留）
del_conf() {
  local n="$1" tmp="$CONF.$$"
  [ -f "$CONF" ] || return 1
  awk -v n="$n" '{ if (NF && $1 !~ /^#/) { c++ ; if (c == n) next } ; print }' "$CONF" > "$tmp" &&
    mv "$tmp" "$CONF"
}

# 改第 n 条配置的某个字段
# field ∈ desc | lip | lport | rip | rport | ruser | rhost | login
# login = 整列替换列4（值形如 user@host 或 host），与 ruser/rhost 二选一使用
set_field() {
  local n="$1" field="$2" val="$3"
  local line parsed c2 c3 c4 desc lip lport rip rport ruser rhost newline tmp

  line=$(conf_line "$n")
  [ -n "$line" ] || return 1
  parsed=$(printf '%s\n' "$line" | parse_line)
  [ -n "$parsed" ] || return 1
  IFS=$'\t' read -r c2 c3 c4 desc <<< "$parsed"

  lip=$(addr_ip "$c2")
  lport=$(addr_port "$c2")
  rip=$(addr_ip "$c3")
  rport=$(addr_port "$c3")
  ruser=$(c4_user "$c4")
  rhost=$(c4_host "$c4")

  case "$field" in
    desc)  desc="$val" ;;
    lip)   lip="$val" ;;
    lport) lport="$val" ;;
    rip)   rip="$val" ;;
    rport) rport="$val" ;;
    ruser) ruser="$val" ;;
    rhost) rhost="$val" ;;
    login) c4="$val" ;;                       # 整列替换，不再按 ruser/rhost 重建
    *)     return 2 ;;
  esac

  [ -z "$lip" ] && lip="127.0.0.1"
  [ -z "$rip" ] && rip="127.0.0.1"
  if [ "$field" != "login" ]; then
    if [ -n "$ruser" ]; then c4="$ruser@$rhost"; else c4="$rhost"; fi
  fi
  newline=$(printf '%s,%s:%s,%s:%s,%s' \
    "$desc" "$lip" "$lport" "$rip" "$rport" "$c4")

  tmp="$CONF.$$"
  awk -v n="$n" -v nl="$newline" \
    '{ if (NF && $1 !~ /^#/) { c++ ; if (c == n) { print nl; next } } ; print }' \
    "$CONF" > "$tmp" && mv "$tmp" "$CONF"
}

# ---------- 本程序起的进程记录 ----------
is_managed() {
  [ -f "$PIDF" ] || return 1
  grep -qx "$1" "$PIDF" 2>/dev/null
}

add_pid() { printf '%s\n' "$1" >> "$PIDF"; }

remove_pid() {
  [ -f "$PIDF" ] || return 0
  local tmp="$PIDF.$$"
  grep -vx "$1" "$PIDF" > "$tmp" 2>/dev/null || :
  # 空文件不留 —— 最后一条隧道断开后，pid 文件自动消失
  if [ -s "$tmp" ]; then mv "$tmp" "$PIDF"; else rm -f "$tmp" "$PIDF"; fi
}

# ---------- 扫描全机 ssh 隧道 ----------
# ps 看 -L / LocalForward（知道转发意图）；lsof 看监听端口（不漏 ssh_config 里的 LocalForward）
# 两者按 pid 合并，最后按 pid 排序（保证编号在状态区与断开区一致）。
# 输出每行：pid<TAB>本地监听<TAB>转发目标<TAB>SSH主机<TAB>持续时间
# 持续时间取自 `ps -o etime`，ps 拿不到时记 "-"
scan_tunnels() {
  {
    lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null |
      awk 'NR>1 && $1=="ssh" { print "L\t" $2 "\t" $9 }'

    ps -axww -o pid=,etime=,command= 2>/dev/null | awk '
      {
        pid = $1; et = $2
        n = split($3, b, "/"); if (b[n] != "ssh") next
        spec = ""
        for (i = 4; i <= NF; i++) {
          a = $i
          if (a == "-L")            { spec = $(i+1); break }
          else if (a ~ /^-L./)      { spec = substr(a, 3); break }
        }
        if (spec == "") {
          for (i = 4; i <= NF; i++) {
            a = $i; nx = $(i+1)
            if ((a == "-o" || a == "-O") && nx ~ /^LocalForward=/) { spec = substr(nx, 14); break }
            if (a ~ /^LocalForward=/)                              { spec = substr(a, 14);  break }
          }
        }
        t = ""
        for (i = NF; i >= 3; i--) { if ($i !~ /^-/) { t = $i; break } }
        print "P\t" pid "\t" spec "\t" t "\t" et
      }'
  } | awk -F'\t' '
    # L（lsof）与 P（ps）两路各自都能独立产生记录：
    # 任一路拿不到数据（例如 ps 被限制、或 ssh_config 里的 LocalForward），
    # 另一路仍然照常输出，不会整张表变空。
    $1 == "L" {
      if (!($2 in seen)) { seen[$2] = 1; order[++np] = $2 }
      ls[$2] = $3
      next
    }
    $1 == "P" {
      if (!($2 in seen)) { seen[$2] = 1; order[++np] = $2 }
      spec[$2] = $3
      tgt[$2]  = $4
      et[$2]   = $5
      next
    }
    END {
      for (k = 1; k <= np; k++) {
        p = order[k]
        s = spec[p]
        have = (p in ls)
        if (s == "" && !have) continue          # 没有转发，不是隧道
        m = split(s, a, ":")
        if (m == 4)      { loc = a[1] ":" a[2]; rem = a[3] ":" a[4] }
        else if (m == 3) { loc = "127.0.0.1:" a[1]; rem = a[2] ":" a[3] }
        else             { loc = "-"; rem = (s == "" ? "-" : s) }
        if (have) loc = ls[p]                   # lsof 权威
        print p "\t" loc "\t" rem "\t" (tgt[p] == "" ? "-" : tgt[p]) "\t" (et[p] == "" ? "-" : et[p])
      }
    }' | sort -k1,1n
}

# 现有隧道 → "<编号><TAB><pid><TAB><本地监听>"
# 现有隧道 → "<编号><TAB><pid><TAB><本地监听><TAB><转发目标><TAB><SSH主机>"
list_existing() {
  local i=0 pid loc rem tgt et
  while IFS=$'\t' read -r pid loc rem tgt et; do
    [ -n "$pid" ] || continue
    i=$((i + 1))
    printf '%s\t%s\t%s\t%s\t%s\n' "$i" "$pid" "$loc" "$rem" "$tgt"
  done < <(scan_tunnels)
}

# 该本地监听端口是否已在运行 → 已运行则输出 pid
is_port_running() {
  local port="${1##*:}"
  list_existing | awk -F'\t' -v p="$port" '{
    k = split($3, a, ":"); if (a[k] == p) { print $2; exit }
  }'
}

# ---------- 状态表附加信息（说明 / 通讯量） ----------

# 配置里「本地端口 → 说明」的映射，供状态表和断开表 join
# 输出：<本地端口><TAB><说明>（说明可含空格，放在最后一列）
conf_port_desc() {
  list_conf | while IFS=$'\t' read -r no raw; do
    parsed=$(printf '%s\n' "$raw" | parse_line)
    [ -n "$parsed" ] || continue
    IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"
    printf '%s\t%s\n' "$(addr_port "$c2")" "$d"
  done
}

# 每个 ssh 进程的收发字节（仅 macOS 有 nettop），输出：<pid><TAB><字节>
# 除以 2：bytes_in + bytes_out 把同一份数据算了两次（本地应用收进一次、再发往远端一次）
traffic_map() {
  command -v nettop >/dev/null 2>&1 || return 0
  nettop -P -x -n -L 1 -p ssh 2>/dev/null | awk -F, '
    NR > 1 && $2 != "" && $5 ~ /^[0-9]+$/ && $6 ~ /^[0-9]+$/ {
      k = split($2, a, "."); printf "%s\t%d\n", a[k], int(($5 + $6) / 2)
    }'
}

# 字节数 → 人读单位
human_bytes() {
  awk -v b="$1" 'BEGIN{
    if (b == "") { print "-"; exit }
    split("B KiB MiB GiB TiB", u, " "); i = 1
    while (b >= 1024 && i < 5) { b /= 1024; i++ }
    if (i == 1) printf "%d %s", b, u[i]; else printf "%.1f %s", b, u[i]
  }'
}

# ---------- 起 / 停 ----------
connect_one() {
  # $1=列2(localip:localport)  $2=列3(remoteip:remoteport)  $3=列4(user@host)
  local c2="$1" c3="$2" c4="$3" pid
  nohup ssh -N -L "$c2:$c3" "$c4" >/dev/null 2>&1 &
  pid=$!
  add_pid "$pid"
  sleep 1
  if kill -0 "$pid" 2>/dev/null; then
    log_result 连接 成功 "$c2" "$c3" "$c4" "$pid"
    printf '已连接：ssh -N -L %s:%s %s   (pid %s)\n' "$c2" "$c3" "$c4" "$pid"
    return 0
  fi
  log_result 连接 失败 "$c2" "$c3" "$c4" "$pid"
  remove_pid "$pid"
  printf '连接失败\n'
  return 1
}

kill_one() {
  local pid="$1" loc="${2:--}" rem="${3:--}" tgt="${4:--}"
  kill "$pid" 2>/dev/null
  sleep 1
  if kill -0 "$pid" 2>/dev/null; then
    log_result 断开 失败 "$loc" "$rem" "$tgt" "$pid"
    printf '断开失败\n'
    return 1
  fi
  log_result 断开 成功 "$loc" "$rem" "$tgt" "$pid"
  remove_pid "$pid"
  printf '已断开 (pid %s)\n' "$pid"
  return 0
}

# ---------- 两个视图（纯输出，TUI 与 CLI 共用同一份） ----------
show_status() {
  printf '================ sshtp ================\n'
  printf '编号 | 本地ip:端口 | 转发目标 | SSH主机 | 持续时间 | 通讯量 | 来源 | PID | 说明\n'
  local any=0 i=0 pid loc rem tgt et src port d tr cmap tmap
  cmap=$(conf_port_desc)                 # 端口 → 说明
  tmap=$(traffic_map)                    # pid  → 收发合计字节
  while IFS=$'\t' read -r pid loc rem tgt et; do
    [ -n "$pid" ] || continue
    any=1; i=$((i + 1))
    src="外部"
    if is_managed "$pid"; then src="本程序"; fi

    port="${loc##*:}"
    d=$(printf '%s\n' "$cmap" | awk -F'\t' -v p="$port" '$1 == p { print $2; exit }')
    [ -n "$d" ] || d="-"                 # 外部起的隧道不在配置里 → -

    tr=$(printf '%s\n' "$tmap" | awk -F'\t' -v p="$pid" '$1 == p { print $2; exit }')
    if [ -n "$tr" ]; then tr=$(human_bytes "$tr"); else tr="-"; fi

    printf '%s | %s | %s | %s | %s | %s | %s | %s | %s\n' \
      "$i" "$loc" "$rem" "$tgt" "$et" "$tr" "$src" "$pid" "$d"
  done < <(scan_tunnels)
  [ "$any" = 0 ] && printf '（没有检测到隧道）\n'
  printf '\n'
}

show_config() {
  printf '================ 配置 ================\n'
  printf '编号 | 本地ip:端口 | 转发目标 | SSH主机 | 说明 | 状态\n'
  local any=0 no raw parsed c2 c3 c4 d already st
  while IFS=$'\t' read -r no raw; do
    [ -n "$no" ] || continue
    any=1
    parsed=$(printf '%s\n' "$raw" | parse_line)
    [ -n "$parsed" ] || continue
    IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"
    already=$(is_port_running "$c2")
    if [ -n "$already" ]; then st="已运行 pid $already"; else st="未运行"; fi
    printf '%s | %s | %s | %s | %s | %s\n' "$no" "$c2" "$c3" "$c4" "$d" "$st"
  done < <(list_conf)
  [ "$any" = 0 ] && printf '（配置里还没有隧道）\n'
  printf '\n'
}

# 键位速查 —— 无参数时与状态一并打印。
# 这是本程序**唯一**的完整说明：有哪些功能、每个键吃几段参数、字段名、编号从哪来。
# 任何调用者（尤其是 agent）裸跑一次就全部拿全，因此不再提供 h / -h / --help。
print_keys() {
  printf '  sshtp —— SSH 隧道管理器（CLI / TUI 同一套键）\n'
  printf '  CLI：sshtp <键> [参数]，跑完即退      TUI：在此按键进菜单，不按键每 %s 秒刷新\n' "$TICK"
  printf '\n'
  printf '  配置   [v] 查看配置\n'
  printf '         [n] 新建 <说明> <本地ip:端口> <远程ip:端口> <user@host>\n'
  printf '         [e] 修改 <编号> <字段> <新值>      [x] 删除 <编号>\n'
  printf '  隧道   [c] 连接 <编号>      [d] 断开 <编号>\n'
  printf '  其他   [r] 刷新（仅 TUI）    [q] 退出（仅 TUI）\n'
  printf '\n'
  printf '  编号：v/e/x/c 用配置行号，d 用状态表编号\n'
  printf '\n'
}

# ==========================================================
#  TUI 前端 —— 交互（clear / read / pause）
# ==========================================================

clr() { clear 2>/dev/null || printf '\033[2J\033[H'; }

pause_key() { printf '\n按任意键返回...'; IFS= read -n 1 -s _ || :; }

do_view() {
  clr
  show_config
  pause_key
}

do_new() {
  local idx def desc lip lport rip rport mode c4
  clr
  printf '新建（逐项输入；此行只写配置，不自动连接）\n\n'
  idx=$(conf_count)
  def=$(printf '%02d' $((idx + 1)))

  printf ' 1) 说明 —— 这条隧道的备注。任意文本，可含空格、不限长短；**不能含逗号**（逗号是列分隔符）\n'
  printf '    留空则自动使用序号：%s\n' "$def"
  printf '    > '; IFS= read -r desc
  [ -z "$desc" ] && desc="$def"

  printf '\n 2) 本地 IP —— 本机监听地址，如 127.0.0.1 或 192.168.10.100\n'
  printf '    留空记 127.0.0.1\n'
  printf '    > '; IFS= read -r lip
  [ -z "$lip" ] && lip="127.0.0.1"

  printf '\n 3) 本地端口 —— 本机监听端口，如 3081（必填）\n'
  printf '    > '; IFS= read -r lport
  if ! is_port "$lport"; then printf '\n本地端口必须是 1-65535 的数字。\n'; pause_key; return; fi

  printf '\n 4) 远程 IP —— 从远端视角解析，如 127.0.0.1 或 10.0.0.5\n'
  printf '    留空记 127.0.0.1\n'
  printf '    > '; IFS= read -r rip
  [ -z "$rip" ] && rip="127.0.0.1"

  printf '\n 5) 远程端口 —— 目标端口，如 3080（必填）\n'
  printf '    > '; IFS= read -r rport
  if ! is_port "$rport"; then printf '\n远程端口必须是 1-65535 的数字。\n'; pause_key; return; fi

  printf '\n 6) 远程登录方式\n'
  printf '    [1] user@host     显式指定用户名，如 changwei@x1c\n'
  printf '    [2] config 名称    走 ~/.ssh/config，不写用户名，如 x1c\n'
  printf '    > '; IFS= read -r mode

  case "$mode" in
    1)
      printf '\n 7) 用户名@主机（必填）\n'
      printf '    > '; IFS= read -r c4
      if [ -z "$c4" ]; then printf '\n不能为空。\n'; pause_key; return; fi
      case "$c4" in
        *@*) : ;;
        *)   printf '\n这种方式要写成 用户名@主机，如 changwei@x1c。\n'; pause_key; return ;;
      esac
      if [ -z "${c4%%@*}" ] || [ -z "${c4#*@}" ]; then
        printf '\n用户名和主机都不能为空。\n'; pause_key; return
      fi
      ;;
    2)
      printf '\n 7) config 名称 —— ~/.ssh/config 里的 Host 别名（必填）\n'
      printf '    > '; IFS= read -r c4
      if [ -z "$c4" ]; then printf '\nconfig 名称不能为空。\n'; pause_key; return; fi
      case "$c4" in
        *@*) printf '\n这种方式不应含 @；要指定用户名请回上一步选 [1]。\n'; pause_key; return ;;
      esac
      ;;
    *)
      printf '\n请输入 1 或 2。\n'; pause_key; return
      ;;
  esac

  local v
  for v in "$desc" "$lip" "$lport" "$rip" "$rport" "$c4"; do
    if has_comma "$v"; then printf '\n任何一段都不能含逗号（逗号是列分隔符）。\n'; pause_key; return; fi
  done

  add_conf "$desc" "$lip" "$lport" "$rip" "$rport" "$c4"
  printf '\n已写入 %s\n' "$CONF"
  pause_key
}

do_edit() {
  local rows n line parsed c2 c3 c4 d lip lport rip rport no raw fno fld fname val mode

  clr
  printf '修改配置（编号 = 配置文件行序号）\n\n'
  rows=$(list_conf)
  if [ -z "$rows" ]; then printf '  配置里还没有隧道，先按 n 新建。\n'; pause_key; return; fi

  while IFS=$'\t' read -r no raw; do
    [ -n "$no" ] || continue
    parsed=$(printf '%s\n' "$raw" | parse_line)
    [ -n "$parsed" ] || continue
    IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"
    printf '  %2s) %s -> %s   %s   (%s)\n' "$no" "$c2" "$c3" "$c4" "$d"
  done <<< "$rows"

  printf '\n输入要修改的编号（直接回车取消）: '
  IFS= read -r n
  [ -z "$n" ] && return
  line=$(conf_line "$n")
  if [ -z "$line" ]; then printf '\n没有第 %s 条。\n' "$n"; pause_key; return; fi

  parsed=$(printf '%s\n' "$line" | parse_line)
  IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"
  lip=$(addr_ip "$c2")
  lport=$(addr_port "$c2")
  rip=$(addr_ip "$c3")
  rport=$(addr_port "$c3")

  clr
  printf '第 %s 条当前内容：\n  %s\n\n' "$n" "$line"
  printf ' 1) 说明        = %s\n' "$d"
  printf ' 2) 本地 IP     = %s\n' "$lip"
  printf ' 3) 本地端口    = %s\n' "$lport"
  printf ' 4) 远程 IP     = %s\n' "$rip"
  printf ' 5) 远程端口    = %s\n' "$rport"
  printf ' 6) 远程登录    = %s\n' "$c4"

  printf '\n输入要改的字段号 1-6（直接回车取消）: '
  IFS= read -r fno
  [ -z "$fno" ] && return
  case "$fno" in
    1) fld=desc  ; fname='说明' ;;
    2) fld=lip   ; fname='本地 IP' ;;
    3) fld=lport ; fname='本地端口' ;;
    4) fld=rip   ; fname='远程 IP' ;;
    5) fld=rport ; fname='远程端口' ;;
    6) fld=login ; fname='远程登录' ;;
    *) printf '\n没有 %s 号字段。\n' "$fno"; pause_key; return ;;
  esac

  if [ "$fld" = "login" ]; then
    printf '\n 新的远程登录方式\n'
    printf '    [1] user@host     显式指定用户名，如 changwei@x1c\n'
    printf '    [2] config 名称    走 ~/.ssh/config，不写用户名，如 x1c\n'
    printf '    > '; IFS= read -r mode
    case "$mode" in
      1)
        printf '    用户名@主机（直接回车取消）: '; IFS= read -r val
        [ -z "$val" ] && return
        case "$val" in
          *@*) : ;;
          *)   printf '\n这种方式要写成 用户名@主机，如 changwei@x1c。\n'; pause_key; return ;;
        esac
        if [ -z "${val%%@*}" ] || [ -z "${val#*@}" ]; then
          printf '\n用户名和主机都不能为空。\n'; pause_key; return
        fi
        ;;
      2)
        printf '    config 名称（直接回车取消）: '; IFS= read -r val
        [ -z "$val" ] && return
        case "$val" in
          *@*) printf '\n这种方式不应含 @；要指定用户名请选 [1]。\n'; pause_key; return ;;
        esac
        ;;
      *)
        printf '\n请输入 1 或 2。\n'; pause_key; return
        ;;
    esac
  else
    printf '\n新的%s（直接回车取消）: ' "$fname"
    IFS= read -r val
    [ -z "$val" ] && return
    if [ "$fld" = "lport" ] || [ "$fld" = "rport" ]; then
      if ! is_port "$val"; then printf '\n端口必须是 1-65535 的数字。\n'; pause_key; return; fi
    fi
  fi

  if has_comma "$val"; then printf '\n新值不能含逗号（逗号是列分隔符）。\n'; pause_key; return; fi

  if set_field "$n" "$fld" "$val"; then
    printf '\n已修改第 %s 条的%s。\n' "$n" "$fname"
  else
    printf '\n修改失败。\n'
  fi
  pause_key
}

do_del() {
  local rows n line no raw parsed c2 c3 c4 d

  clr
  printf '删除配置（编号 = 配置文件行序号；只删配置，不断隧道）\n\n'
  rows=$(list_conf)
  if [ -z "$rows" ]; then printf '  配置里还没有隧道。\n'; pause_key; return; fi

  while IFS=$'\t' read -r no raw; do
    [ -n "$no" ] || continue
    parsed=$(printf '%s\n' "$raw" | parse_line)
    [ -n "$parsed" ] || continue
    IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"
    printf '  %2s) %s -> %s   %s   (%s)\n' "$no" "$c2" "$c3" "$c4" "$d"
  done <<< "$rows"

  printf '\n输入要删除的编号（直接回车取消）: '
  IFS= read -r n
  [ -z "$n" ] && return
  line=$(conf_line "$n")
  if [ -z "$line" ]; then printf '\n没有第 %s 条。\n' "$n"; pause_key; return; fi

  del_conf "$n"
  printf '\n已删除第 %s 条：%s\n' "$n" "$line"
  pause_key
}

do_connect() {
  local rows n line parsed c2 c3 c4 d no raw already st

  clr
  printf '连接隧道（编号 = 配置文件行序号）\n\n'
  rows=$(list_conf)
  if [ -z "$rows" ]; then printf '  配置里还没有隧道，先按 n 新建。\n'; pause_key; return; fi

  while IFS=$'\t' read -r no raw; do
    [ -n "$no" ] || continue
    parsed=$(printf '%s\n' "$raw" | parse_line)
    [ -n "$parsed" ] || continue
    IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"
    already=$(is_port_running "$c2")
    if [ -n "$already" ]; then st="已运行 pid $already"; else st="未运行"; fi
    printf '  %2s) %s -> %s   %s   (%s)   [%s]\n' "$no" "$c2" "$c3" "$c4" "$d" "$st"
  done <<< "$rows"

  printf '\n输入序号连接（直接回车取消）: '
  IFS= read -r n
  [ -z "$n" ] && return
  line=$(conf_line "$n")
  if [ -z "$line" ]; then printf '\n没有第 %s 条。\n' "$n"; pause_key; return; fi
  parsed=$(printf '%s\n' "$line" | parse_line)
  IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"

  already=$(is_port_running "$c2")
  if [ -n "$already" ]; then
    printf '\n该隧道已在运行（pid %s），不重复连接。\n' "$already"
    pause_key; return
  fi

  printf '\n'
  connect_one "$c2" "$c3" "$c4"
  pause_key
}

do_disconnect() {
  local i=0 pid loc rem tgt et n port d cmap
  local pids=() locs=() rems=() tgts=()

  clr
  printf '断开隧道（编号 = 现有隧道，含外部）\n\n'
  cmap=$(conf_port_desc)
  while IFS=$'\t' read -r pid loc rem tgt et; do
    [ -n "$pid" ] || continue
    i=$((i + 1))
    pids[$i]="$pid"; locs[$i]="$loc"; rems[$i]="$rem"; tgts[$i]="$tgt"
    port="${loc##*:}"
    d=$(printf '%s\n' "$cmap" | awk -F'\t' -v p="$port" '$1 == p { print $2; exit }')
    [ -n "$d" ] || d="-"
    printf '  %2d) %s -> %s   %s   (pid %s)   (%s)\n' "$i" "$loc" "$rem" "$tgt" "$pid" "$d"
  done < <(scan_tunnels)

  if [ "$i" = 0 ]; then
    printf '  没有检测到隧道。\n'
    pause_key; return
  fi
  printf '\n输入编号断开（直接回车取消）: '
  IFS= read -r n
  [ -z "$n" ] && return
  pid="${pids[$n]:-}"
  if [ -z "$pid" ]; then printf '\n没有第 %s 项。\n' "$n"; pause_key; return; fi

  printf '\n'
  kill_one "$pid" "${locs[$n]:--}" "${rems[$n]:--}" "${tgts[$n]:--}"
  pause_key
}

# main_menu [首键]
#   带参数时，第一轮直接执行该键（用于"无参数入口 3 秒内按键直接进菜单"），
#   之后转入正常读键循环；不按键则每 TICK 秒自动重绘一次（状态表要实时刷新）。
main_menu() {
  local key="${1:-}" preset=0 rc
  [ -n "$key" ] && preset=1
  while :; do
    clr
    show_status
    print_keys
    printf '> '
    if [ "$preset" = 1 ]; then
      preset=0
      printf '\n'
    else
      IFS= read -r -n 1 -s -t "$TICK" key
      rc=$?
      printf '\n'
      if [ "$rc" -ne 0 ]; then
        # 超时（bash 3.2 里 `read -t` 超时与 EOF 同为非 0，实测）→ 视作"没按键"，
        # 重绘后继续。菜单只在 stdin 是终端时进入；真遇到 EOF（终端被关）则退出，避免空转。
        [ -t 0 ] || break
        continue
      fi
    fi
    case "$key" in
      r|R) : ;;                              # 刷新 = 重绘（每轮循环本就整屏重绘）
      v|V) do_view ;;
      n|N) do_new ;;
      e|E) do_edit ;;
      x|X) do_del ;;
      c|C) do_connect ;;
      d|D) do_disconnect ;;
      q|Q) break ;;
      *)   : ;;
    esac
  done
}

# ==========================================================
#  CLI 前端 —— 一次性执行后退出（不 clear、不 read、不 pause）
# ==========================================================

# 仅供参数不齐 / 未知子命令时的错误提示使用（本程序没有 help 子命令）。
usage() {
  cat <<'EOF'
用法（键位与 TUI 菜单一致）：
  sshtp          状态 + 键位与参数形状；3 秒内按任意键进 TUI 菜单，否则退出
  sshtp v        查看配置
  sshtp n <说明> <本地ip:端口> <远程ip:端口> <user@host>
  sshtp e <编号> <字段> <新值>
  sshtp x <编号>
  sshtp c <编号>
  sshtp d <编号>
EOF
}

cli_view() { show_config; }

cli_new() {
  if [ $# -ne 4 ]; then
    printf '参数不齐：需要 4 个（说明 本地ip:端口 远程ip:端口 user@host），收到 %s 个\n' "$#" >&2
    usage >&2
    exit 1
  fi
  local desc="$1" c2="$2" c3="$3" c4="$4"
  local lip lport rip rport rhost

  lip=$(addr_ip "$c2")
  lport=$(addr_port "$c2")
  rip=$(addr_ip "$c3")
  rport=$(addr_port "$c3")
  rhost=$(c4_host "$c4")

  if ! is_port "$lport"; then printf '本地端口无效：%s\n' "$lport" >&2; exit 1; fi
  if ! is_port "$rport"; then printf '远程端口无效：%s\n' "$rport" >&2; exit 1; fi
  if [ -z "$c4" ]; then printf '第 4 段不能为空（形如 user@host 或 host）\n' >&2; exit 1; fi
  if [ -z "$rhost" ]; then printf '远程主机不能为空（第 4 段形如 user@host 或 host）\n' >&2; exit 1; fi
  case "$c4" in
    @*) printf '远程用户名不可省略（第 4 段不能写成 @host）\n' >&2; exit 1 ;;
  esac

  [ -z "$desc" ] && desc=$(printf '%02d' $(($(conf_count) + 1)))
  [ -z "$lip" ]  && lip="127.0.0.1"
  [ -z "$rip" ]  && rip="127.0.0.1"

  local v
  for v in "$desc" "$c2" "$c3" "$c4"; do
    if has_comma "$v"; then printf '第 4 段等参数不能含逗号（逗号是列分隔符）\n' >&2; exit 1; fi
  done

  add_conf "$desc" "$lip" "$lport" "$rip" "$rport" "$c4"
  printf '已写入 %s\n' "$CONF"
}

cli_edit() {
  if [ $# -ne 3 ]; then
    printf '参数不齐：需要 3 个（编号 字段 新值），收到 %s 个\n' "$#" >&2
    usage >&2
    exit 1
  fi
  local n="$1" fld="$2" val="$3" line
  line=$(conf_line "$n")
  [ -n "$line" ] || { printf '没有第 %s 条配置\n' "$n" >&2; exit 1; }
  case "$fld" in
    desc|lip|lport|rip|rport|ruser|rhost|login) : ;;
    *) printf '未知字段：%s（可用：desc lip lport rip rport login ruser rhost）\n' "$fld" >&2; exit 1 ;;
  esac
  if [ -z "$val" ]; then
    case "$fld" in
      lip|rip) val="127.0.0.1" ;;
      *) printf '字段 %s 不能为空\n' "$fld" >&2; exit 1 ;;
    esac
  fi
  if [ "$fld" = "login" ]; then
    case "$val" in
      @*|*@) printf '远程登录不能写成 %s（形如 user@host 或 host）\n' "$val" >&2; exit 1 ;;
    esac
  fi
  if [ "$fld" = "lport" ] || [ "$fld" = "rport" ]; then
    if ! is_port "$val"; then printf '端口无效：%s\n' "$val" >&2; exit 1; fi
  fi
  if has_comma "$val"; then printf '新值不能含逗号（逗号是列分隔符）\n' >&2; exit 1; fi
  set_field "$n" "$fld" "$val" || { printf '修改失败\n' >&2; exit 1; }
  printf '已修改第 %s 条的 %s\n' "$n" "$fld"
}

cli_del() {
  if [ $# -ne 1 ]; then
    printf '参数不齐：需要 1 个（编号）\n' >&2
    usage >&2
    exit 1
  fi
  local n="$1" line
  line=$(conf_line "$n")
  [ -n "$line" ] || { printf '没有第 %s 条配置\n' "$n" >&2; exit 1; }
  del_conf "$n" || { printf '删除失败\n' >&2; exit 1; }
  printf '已删除第 %s 条：%s\n' "$n" "$line"
}

cli_connect() {
  if [ $# -ne 1 ]; then
    printf '参数不齐：需要 1 个（编号）\n' >&2
    usage >&2
    exit 1
  fi
  local n="$1" line parsed c2 c3 c4 d already
  line=$(conf_line "$n")
  [ -n "$line" ] || { printf '没有第 %s 条配置\n' "$n" >&2; exit 1; }
  parsed=$(printf '%s\n' "$line" | parse_line)
  [ -n "$parsed" ] || { printf '第 %s 条格式不正确\n' "$n" >&2; exit 1; }
  IFS=$'\t' read -r c2 c3 c4 d <<< "$parsed"

  already=$(is_port_running "$c2")
  if [ -n "$already" ]; then
    printf '该隧道已在运行（pid %s），不重复连接。\n' "$already" >&2
    exit 1
  fi
  connect_one "$c2" "$c3" "$c4" || exit 1
}

cli_disconnect() {
  if [ $# -ne 1 ]; then
    printf '参数不齐：需要 1 个（编号）\n' >&2
    usage >&2
    exit 1
  fi
  local n="$1" pid loc rem tgt row
  row=$(list_existing | awk -F'\t' -v n="$n" '$1==n { print $2 "\t" $3 "\t" $4 "\t" $5; exit }')
  [ -n "$row" ] || { printf '没有第 %s 项隧道\n' "$n" >&2; exit 1; }
  IFS=$'\t' read -r pid loc rem tgt <<< "$row"
  kill_one "$pid" "$loc" "$rem" "$tgt" || exit 1
}

# ==========================================================
#  入口
# ==========================================================
cmd="${1:-}"
case "$cmd" in
  "")
    # 无参数 = 打印「状态 + 全部功能/参数/快捷键」，然后开一个 3 秒按键窗口：
    #   窗口内按任意键 → 进菜单（该键直接生效）
    #   窗口内无输入   → 退出（状态与键位表已打印完毕，无需再按键）
    # 一次输出即自举：任何调用者（含 agent）裸跑一次就能拿全信息，且必定自己返回。
    # stdin 不是终端时（管道 / </dev/null），read -t 立即返回，不会白等 3 秒。
    show_status
    print_keys
    printf '> '
    IFS= read -r -n 1 -s -t 3 key
    rc=$?
    printf '\n'
    if [ "$rc" -eq 0 ]; then
      main_menu "$key"
    fi
    ;;
  v)                cli_view ;;
  n)                shift; cli_new "$@" ;;
  e)                shift; cli_edit "$@" ;;
  x)                shift; cli_del "$@" ;;
  c)                shift; cli_connect "$@" ;;
  d)                shift; cli_disconnect "$@" ;;
  h|help|-h|--help)
    printf '本程序没有 help 子命令：直接敲 sshtp（无参数）即打印全部功能、参数与字段名。\n' >&2
    exit 1 ;;
  *)
    printf '未知子命令：%s\n' "$cmd" >&2
    printf '（直接敲 sshtp（无参数）即打印全部功能、参数与字段名）\n' >&2
    exit 1 ;;
esac
