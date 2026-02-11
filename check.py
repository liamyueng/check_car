#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import sys
import re
import time
import unicodedata
import paramiko
from concurrent.futures import ThreadPoolExecutor, as_completed

# ===== 固定配置（明文写死） =====
HOSTS = ["192.168.30.143", "192.168.30.41", "192.168.30.43"]

USERNAME = "root"
PASSWORD = "Abcd12#$"
PORT = 22

NAS_USER = "admin123"
NAS_PASS = "Huawei123"

CONNECT_TIMEOUT = 8
CMD_TIMEOUT = 8
PMUPLOAD_TIMEOUT = 20

# ✅ mount 必须包 timeout，避免卡死
MOUNT_TIMEOUT_SEC = 8

MDC1_IP = "192.168.30.41"    # MDC1A
MDC2_IP = "192.168.30.143"   # MDC2

NAS_160 = "192.168.79.160"   # MDC1A NAS
NAS_60  = "192.168.79.60"    # MDC2 NAS

MOUNT_POINT = "/mnt/share"
MIN_AVAIL_GB = 800

# Topic 映射（最终版）
MDC1_TOPIC_CMDS = [
    ("4. MDC1A 左侧 DTOF",           "timeout 8s pmupload adstopic hz /dtof_left"),
    ("5. MDC1A 右侧 DTOF",           "timeout 8s pmupload adstopic hz /dtof_right"),
    ("6. MDC1A 后向 DTOF",           "timeout 8s pmupload adstopic hz /dtof_rear"),
    ("7. MDC1A 感知目标列表",        "timeout 8s pmupload adstopic hz /object_array"),
    ("8. MDC1A 融合感知目标列表",    "timeout 8s pmupload adstopic hz /object_array_fusion"),
    ("9. MDC1A 前向激光雷达",        "timeout 8s pmupload adstopic hz /lidar_side_front"),
]

MDC2_TOPIC_CMDS = [
    ("10. MDC2 后向激光雷达",   "timeout 8s pmupload adstopic hz /lidar_side_rear"),
    ("11. MDC2 右侧激光雷达",   "timeout 8s pmupload adstopic hz /lidar_side_right"),
    ("12. MDC2 车顶激光雷达",   "timeout 8s pmupload adstopic hz /lidar_side_roof"),
    ("13. MDC2 左侧激光雷达",   "timeout 8s pmupload adstopic hz /lidar_side_left"),
]

# 并发限制（避免 pmupload 并发把输出打坏）
MDC1_MAX_WORKERS = 2
MDC2_MAX_WORKERS = 4
# ====================================

# ANSI colors（Windows Terminal/PowerShell 通常支持）
GREEN = "\033[92m"
RED = "\033[91m"
RESET = "\033[0m"
OK = f"{GREEN}√{RESET}"
FAIL = f"{RED}X{RESET}"

ANSI_RE = re.compile(r"\x1b\[[0-9;]*m")

# ---------- 中文宽度对齐 ----------
def visual_width(s: str) -> int:
    s = ANSI_RE.sub("", s)
    w = 0
    for ch in s:
        w += 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
    return w

def pad_left(s: str, width: int) -> str:
    return s + (" " * max(0, width - visual_width(s)))

def print_table(rows: list[tuple[str, str, str]]) -> None:
    header = ("检测项", "状态", "提醒")
    w1 = max(visual_width(header[0]), *(visual_width(r[0]) for r in rows))
    w2 = max(visual_width(header[1]), *(visual_width(r[1]) for r in rows))
    w3 = max(visual_width(header[2]), *(visual_width(r[2]) for r in rows))

    print(f"{pad_left(header[0], w1)}  {pad_left(header[1], w2)}  {pad_left(header[2], w3)}")
    print(f"{'-'*w1}  {'-'*w2}  {'-'*w3}")
    for a, b, c in rows:
        print(f"{pad_left(a, w1)}  {pad_left(b, w2)}  {pad_left(c, w3)}")

def clear_screen() -> None:
    if sys.platform.startswith("win"):
        import os
        os.system("cls")
    else:
        print("\033c", end="")

def read_key() -> str:
    if sys.platform.startswith("win"):
        import msvcrt
        ch = msvcrt.getch()
        if ch in (b"\x00", b"\xe0"):
            _ = msvcrt.getch()
            return ""
        return ch.decode(errors="ignore").lower()
    else:
        return input().strip().lower()[:1]

# ---------- SSH helpers ----------
def ssh_connect(host: str) -> paramiko.SSHClient:
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(
        hostname=host,
        port=PORT,
        username=USERNAME,
        password=PASSWORD,
        timeout=CONNECT_TIMEOUT,
        banner_timeout=CONNECT_TIMEOUT,
        auth_timeout=CONNECT_TIMEOUT,
        look_for_keys=False,
        allow_agent=False,
    )
    return c

def try_ssh(host: str) -> bool:
    try:
        c = ssh_connect(host)
        c.close()
        return True
    except Exception:
        return False

def exec_cmd(client: paramiko.SSHClient, cmd: str, timeout_sec: int, get_pty: bool = False) -> tuple[int, str, str]:
    _, stdout, stderr = client.exec_command(cmd, timeout=timeout_sec, get_pty=get_pty)
    out = stdout.read().decode("utf-8", errors="replace")
    err = stderr.read().decode("utf-8", errors="replace")
    code = stdout.channel.recv_exit_status()
    return code, out, err

# ---------- df / mount ----------
def parse_size_to_gb(size_str: str) -> float | None:
    s = size_str.strip()
    m = re.match(r"^([0-9]*\.?[0-9]+)\s*([KMGTP])([iI]?)B?$", s)
    if not m:
        return None
    val = float(m.group(1))
    unit = m.group(2).upper()
    factor = {"K": 1/(1024*1024), "M": 1/1024, "G": 1.0, "T": 1024.0, "P": 1024.0*1024.0}[unit]
    return val * factor

def df_find_mount_avail(df_out: str, nas_ip: str) -> tuple[bool, str | None, float | None]:
    for line in df_out.splitlines():
        if nas_ip in line:
            parts = line.split()
            if len(parts) >= 6:
                avail_str = parts[3]
                avail_gb = parse_size_to_gb(avail_str)
                return True, avail_str, avail_gb
            return True, None, None
    return False, None, None

def build_mount_cmd(nas_ip: str) -> str:
    # ✅ 所有 mount 必须包 timeout，且 -o 参数不能有空格
    mount_opts = (
        f"vers=2.0,username={NAS_USER},password={NAS_PASS},cache=strict,"
        f"uid=1000,forceuid,gid=1000,forcegid,"
        f"file_mode=0755,dir_mode=0755,soft,nounix,noserverino,mapposix,"
        f"rsize=65536,wsize=65536,bsize=1048576,echo_interval=60,actimeo=1"
    )
    return (
        f"mkdir -p {MOUNT_POINT}; "
        f"umount -l {MOUNT_POINT} 2>/dev/null || true; "
        f"timeout {MOUNT_TIMEOUT_SEC}s mount -t cifs //{nas_ip}/nas {MOUNT_POINT} -o {mount_opts}"
    )

def check_mount_alive(client: paramiko.SSHClient) -> bool:
    # ✅ 必须用 ls 判定真实可用，避免 stale 假挂
    code1, _, _ = exec_cmd(client, f"ls {MOUNT_POINT} >/dev/null 2>&1", CMD_TIMEOUT, get_pty=True)
    if code1 != 0:
        return False
    # 最小写入测试（可用性判定）
    code2, _, _ = exec_cmd(
        client,
        f"touch {MOUNT_POINT}/.__nas_test__ && rm -f {MOUNT_POINT}/.__nas_test__ >/dev/null 2>&1",
        CMD_TIMEOUT,
        get_pty=True,
    )
    return code2 == 0

def ensure_mount_and_get_df(host: str, nas_ip: str) -> tuple[bool, str, bool]:
    """
    ✅ 只尝试一次“umount -l + mount”（无循环重试）
    - 如果当前就可用：直接 PASS
    - 否则：清理+重挂一次，再判断
    """
    did_mount_attempt = False
    try:
        c = ssh_connect(host)

        # 先看 df + alive
        _, df_out, _ = exec_cmd(c, "df -h", CMD_TIMEOUT)
        mounted, _, avail_gb = df_find_mount_avail(df_out, nas_ip)
        if mounted and avail_gb is not None and avail_gb >= MIN_AVAIL_GB and check_mount_alive(c):
            c.close()
            return True, df_out, False

        # 不可用 -> 只重挂一次
        did_mount_attempt = True
        exec_cmd(c, build_mount_cmd(nas_ip), CMD_TIMEOUT, get_pty=True)

        _, df_out2, _ = exec_cmd(c, "df -h", CMD_TIMEOUT)
        mounted2, _, avail_gb2 = df_find_mount_avail(df_out2, nas_ip)
        ok = mounted2 and (avail_gb2 is not None) and (avail_gb2 >= MIN_AVAIL_GB) and check_mount_alive(c)
        c.close()
        return ok, df_out2, True

    except Exception:
        return False, "", did_mount_attempt

def check_mount_row_with_auto_mount(item: str, host: str, nas_ip: str, missing_tip: str) -> tuple[bool, tuple[str, str, str]]:
    mounted, df_out, did_try = ensure_mount_and_get_df(host, nas_ip)

    if not mounted:
        # ✅ 不重试：失败直接提示换盘
        return False, (item, FAIL, "挂载失败或盘不可用（已自动清理并重挂一次），请换盘。")

    m, avail_str, avail_gb = df_find_mount_avail(df_out, nas_ip)
    if not m or avail_str is None or avail_gb is None:
        return False, (item, FAIL, "盘状态异常，请换盘。")
    if avail_gb < MIN_AVAIL_GB:
        return False, (item, FAIL, f"可用容量 {avail_str}（<800G），请换盘。")

    return True, (item, OK, f"可用容量 {avail_str}")

# ---------- pmupload parsing ----------
PMUPLOAD_WINDOW_RE = re.compile(r"^\s*/\S+.*\s(\d+)\s*$")

def parse_pmupload_windows(text: str) -> list[int]:
    windows: list[int] = []
    for line in text.splitlines():
        if not line.strip():
            continue
        if not line.lstrip().startswith("/"):
            continue
        m = PMUPLOAD_WINDOW_RE.match(line)
        if m:
            try:
                windows.append(int(m.group(1)))
            except ValueError:
                pass
    return windows

def run_pmupload_check(host: str, item_name: str, cmd: str) -> tuple[str, tuple[str, str, str], bool]:
    """
    pmupload 不动：保留你已验证的逻辑
    /lidar_side_front 失败提示驾驶员挂D档并踩住刹车
    """
    def run_once(get_pty: bool) -> list[int]:
        c = ssh_connect(host)
        _code, out, err = exec_cmd(c, cmd, PMUPLOAD_TIMEOUT, get_pty=get_pty)
        c.close()
        merged = (out or "") + ("\n" if out and err else "") + (err or "")
        return parse_pmupload_windows(merged) if merged.strip() else []

    try:
        windows = run_once(get_pty=False)
        if (not windows) or all(w == 0 for w in windows):
            windows = run_once(get_pty=True)
    except Exception:
        windows = []

    tip_list = f"windows={windows}"

    def add_drive_tip_prefix(tip: str) -> str:
        if "/lidar_side_front" in cmd:
            return f"请驾驶员挂D档并踩住刹车，{tip}"
        return tip

    if not windows:
        tip = f"可能并发过高/Topic未发布/跑错IP | {cmd} | {tip_list}"
        return item_name, (item_name, FAIL, add_drive_tip_prefix(tip)), False

    if any(w == 0 for w in windows):
        tip = f"{cmd} | {tip_list}"
        return item_name, (item_name, FAIL, add_drive_tip_prefix(tip)), False

    return item_name, (item_name, OK, tip_list), True

def run_pmupload_group(host: str, items: list[tuple[str, str]], max_workers: int) -> tuple[list[tuple[str, str, str]], bool]:
    ordered_names = [name for name, _ in items]
    row_map: dict[str, tuple[str, str, str]] = {}
    ok_map: dict[str, bool] = {}

    max_workers = min(max_workers, max(1, len(items)))
    with ThreadPoolExecutor(max_workers=max_workers) as ex:
        futs = {ex.submit(run_pmupload_check, host, name, cmd): name for name, cmd in items}
        for fut in as_completed(futs):
            name, row, ok = fut.result()
            row_map[name] = row
            ok_map[name] = ok

    rows_in_order = [row_map[name] for name in ordered_names]
    all_ok = all(ok_map.get(name, False) for name in ordered_names)
    return rows_in_order, all_ok

# ---------- failed-only (X) ----------
def is_fail_status(status: str) -> bool:
    return "X" in ANSI_RE.sub("", status)

def filter_failed_items(rows: list[tuple[str, str, str]]) -> set[str]:
    return {item for item, status, _ in rows if is_fail_status(status)}

def run_full_check() -> tuple[bool, list[tuple[str, str, str]]]:
    print("开始检测...预计一分钟。")
    rows: list[tuple[str, str, str]] = []

    car_ok = all(try_ssh(h) for h in HOSTS)
    rows.append(("1. 车机状态", OK if car_ok else FAIL, "" if car_ok else "请上电或插上网线"))
    if not car_ok:
        return False, rows

    mdc1_ok, row2 = check_mount_row_with_auto_mount(
        item=f"2. {MDC1_IP} MDC1A",
        host=MDC1_IP,
        nas_ip=NAS_160,
        missing_tip="未检测到 160 盘。",
    )
    rows.append(row2)

    mdc2_ok, row3 = check_mount_row_with_auto_mount(
        item=f"3. {MDC2_IP} MDC2",
        host=MDC2_IP,
        nas_ip=NAS_60,
        missing_tip="未检测到 60 盘。",
    )
    rows.append(row3)

    mdc1_rows, mdc1_topics_ok = run_pmupload_group(MDC1_IP, MDC1_TOPIC_CMDS, max_workers=MDC1_MAX_WORKERS)
    rows.extend(mdc1_rows)

    mdc2_rows, mdc2_topics_ok = run_pmupload_group(MDC2_IP, MDC2_TOPIC_CMDS, max_workers=MDC2_MAX_WORKERS)
    rows.extend(mdc2_rows)

    all_success = car_ok and mdc1_ok and mdc2_ok and mdc1_topics_ok and mdc2_topics_ok
    return all_success, rows

def run_failed_only_check(prev_rows: list[tuple[str, str, str]]) -> tuple[bool, list[tuple[str, str, str]]]:
    failed = filter_failed_items(prev_rows)
    if not failed:
        return True, []

    if "1. 车机状态" in failed:
        rows = []
        car_ok = all(try_ssh(h) for h in HOSTS)
        rows.append(("1. 车机状态", OK if car_ok else FAIL, "" if car_ok else "请上电或插上网线"))
        return car_ok, rows

    rows: list[tuple[str, str, str]] = []
    all_ok = True

    item2 = f"2. {MDC1_IP} MDC1A"
    if item2 in failed:
        ok2, row2 = check_mount_row_with_auto_mount(item2, MDC1_IP, NAS_160, "未检测到 160 盘。")
        rows.append(row2)
        all_ok = all_ok and ok2

    item3 = f"3. {MDC2_IP} MDC2"
    if item3 in failed:
        ok3, row3 = check_mount_row_with_auto_mount(item3, MDC2_IP, NAS_60, "未检测到 60 盘。")
        rows.append(row3)
        all_ok = all_ok and ok3

    def run_selected(host: str, items: list[tuple[str, str]], max_workers: int) -> tuple[list[tuple[str, str, str]], bool]:
        selected = [(name, cmd) for name, cmd in items if name in failed]
        if not selected:
            return [], True

        ordered = [name for name, _ in selected]
        row_map = {}
        ok_map = {}

        max_workers = min(max_workers, max(1, len(selected)))
        with ThreadPoolExecutor(max_workers=max_workers) as ex:
            futs = {ex.submit(run_pmupload_check, host, name, cmd): name for name, cmd in selected}
            for fut in as_completed(futs):
                name, row, ok = fut.result()
                row_map[name] = row
                ok_map[name] = ok

        rows_ord = [row_map[n] for n in ordered]
        ok_all = all(ok_map.get(n, False) for n in ordered)
        return rows_ord, ok_all

    m1_rows, m1_ok = run_selected(MDC1_IP, MDC1_TOPIC_CMDS, MDC1_MAX_WORKERS)
    rows.extend(m1_rows)
    all_ok = all_ok and m1_ok

    m2_rows, m2_ok = run_selected(MDC2_IP, MDC2_TOPIC_CMDS, MDC2_MAX_WORKERS)
    rows.extend(m2_rows)
    all_ok = all_ok and m2_ok

    return all_ok, rows

def main() -> None:
    last_rows: list[tuple[str, str, str]] = []
    while True:
        clear_screen()
        ok, last_rows = run_full_check()
        print_table(last_rows)

        if ok:
            print("车辆正常，可以正常采集驾驶信息。")
            return

        print("按 R 重启全量检测，按 X 只检测失败项，按 Q 退出。")
        while True:
            k = read_key()
            if k == "r":
                break
            if k == "q":
                return
            if k == "x":
                clear_screen()
                print("开始检测失败项...预计一分钟。")
                ok_failed, rows_failed = run_failed_only_check(last_rows)
                if rows_failed:
                    print_table(rows_failed)
                else:
                    print("无失败项需要复检。")
                if ok_failed:
                    print("车辆正常，可以正常采集驾驶信息。")
                    return
                print("按 R 重启全量检测，按 X 继续只检测失败项，按 Q 退出。")
                while True:
                    k2 = read_key()
                    if k2 in ("r", "x", "q"):
                        k = k2
                        break
                if k == "r":
                    break
                if k == "q":
                    return
                if k == "x":
                    last_rows = rows_failed
                    continue

if __name__ == "__main__":
    main()
