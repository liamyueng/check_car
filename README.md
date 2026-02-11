# check_car - 车辆采集前环境健康检查工具

## 1. 背景与目标

本工具用于车辆采集驾驶数据前的环境健康检查，确保车机、MDC、NAS、Topic 发布状态满足采集条件。  
工具会自动输出检测表格，并支持失败项复检，以减少重复检测耗时。

目标：检测完成后给出明确结论：

- ✅ 车辆正常，可以正常采集驾驶信息
- ❌ 存在异常，提示需要的操作（上电、插网线、换盘、挂 D 档踩刹车等）

---

## 2. 多语言版本

本工具提供三种语言版本：

| 版本 | 文件 | 说明 |
|------|------|------|
| Python | `check.py` | 原始版本，需要 Python 环境 |
| Go | `check.go` | 编译为静态可执行文件，支持 Linux/Windows |
| C++ | `check.cpp` | 编译为静态可执行文件，仅支持 Linux |

---

## 3. 编译说明

### 3.1 使用 Makefile 编译（推荐）

```bash
# 编译所有版本
make all

# 仅编译 Go 版本 (Linux + Windows)
make go

# 仅编译 C++ 版本 (Linux)
make cpp

# 清理编译产物
make clean
```

编译产物位于 `build/` 目录：
- `build/check_linux` - Go Linux 静态可执行文件
- `build/check.exe` - Go Windows 静态可执行文件  
- `build/check_cpp` - C++ Linux 静态可执行文件

### 3.2 手动编译 Go 版本

```bash
# 安装依赖
go mod init check_car
go get golang.org/x/crypto/ssh

# 编译 Linux 静态版本
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o check_linux check.go

# 编译 Windows 静态版本
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o check.exe check.go
```

### 3.3 手动编译 C++ 版本

```bash
# 安装依赖 (Debian/Ubuntu)
sudo apt-get install -y libssh2-1-dev libssl-dev zlib1g-dev

# 静态编译 (如果静态库可用)
g++ -std=c++20 -O2 -static -o check_cpp check.cpp -lssh2 -lssl -lcrypto -lpthread -ldl -lz

# 动态编译 (备选)
g++ -std=c++20 -O2 -o check_cpp check.cpp -lssh2 -lssl -lcrypto -lpthread
```

---

## 4. 运行环境要求

### 4.1 Python 版本
- Python 3.9+
- 依赖库：`paramiko`

```bash
pip install paramiko
python check.py
```

### 4.2 Go 版本
- 编译后为静态可执行文件，无需额外依赖
- 支持 Linux 和 Windows

```bash
./check_linux      # Linux
check.exe          # Windows
```

### 4.3 C++ 版本
- 静态编译后无需额外依赖
- 动态编译需要 libssh2 运行时库

```bash
./check_cpp
```

---

## 5. 原始 Python 依赖

Python 版本依赖：

- `paramiko`
- Python 标准库：
  - `sys`
  - `re`
  - `time`
  - `unicodedata`
  - `concurrent.futures`


---

## 3. 固定配置参数（脚本内写死）

### 3.1 SSH 配置

| 参数 | 值 |
|------|----|
| USERNAME | `root` |
| PASSWORD | `Abcd12#$` |
| PORT | `22` |
| CONNECT_TIMEOUT | `8` 秒 |
| CMD_TIMEOUT | `8` 秒 |
| PMUPLOAD_TIMEOUT | `20` 秒 |

---

### 3.2 车机 IP 列表

脚本会检查以下 3 台车机是否能 SSH 连接：

```text
192.168.30.143
192.168.30.41
192.168.30.43
```

---

### 3.3 MDC 与 NAS IP 配置

| 组件 | IP |
|------|----|
| MDC1A | `192.168.30.41` |
| MDC2 | `192.168.30.143` |
| MDC1A NAS | `192.168.79.160` |
| MDC2 NAS | `192.168.79.60` |

---

### 3.4 NAS 挂载配置

| 参数 | 值 |
|------|----|
| MOUNT_POINT | `/mnt/share` |
| MOUNT_TIMEOUT_SEC | `8` 秒 |
| MIN_AVAIL_GB | `800GB` |
| NAS_USER | `admin123` |
| NAS_PASS | `Huawei123` |

挂载命令使用 `mount -t cifs`，并且必须使用 `timeout` 包裹防止卡死。

---

### 3.5 Topic 检测命令（pmupload）

#### MDC1A Topic 列表（6 项）

| 检测项 | Topic |
|--------|-------|
| 4 | `/dtof_left` |
| 5 | `/dtof_right` |
| 6 | `/dtof_rear` |
| 7 | `/object_array` |
| 8 | `/object_array_fusion` |
| 9 | `/lidar_side_front` |

命令格式：

```bash
timeout 8s pmupload adstopic hz <topic>
```

---

#### MDC2 Topic 列表（4 项）

| 检测项 | Topic |
|--------|-------|
| 10 | `/lidar_side_rear` |
| 11 | `/lidar_side_right` |
| 12 | `/lidar_side_roof` |
| 13 | `/lidar_side_left` |

命令格式同上。

---

### 3.6 并发限制

为了避免 `pmupload` 并发执行导致输出混乱，脚本对并发做限制：

| 组件 | 最大并发 |
|------|----------|
| MDC1A | 2 |
| MDC2 | 4 |


---

## 4. 输出格式要求

### 4.1 表格字段

输出表格包含三列：

| 检测项 | 状态 | 提醒 |
|--------|------|------|

### 4.2 状态符号

- 成功：绿色 `√`
- 失败：红色 `X`

### 4.3 中文宽度对齐

脚本必须正确对齐中英文混排表格，规则：

- East Asian Width 属于 `W/F` 的字符宽度为 2
- 其它字符宽度为 1
- ANSI 颜色码不计入宽度


---

## 5. 功能需求（检测项逻辑）

脚本检测项必须按固定顺序输出：

---

### 5.1 检测项 1：车机状态（SSH 可用性检测）

#### 检测逻辑
- 遍历 `HOSTS` 中所有 IP
- 尝试 SSH 连接（只验证能连上）
- 任意失败则判定失败

#### 输出要求
- 成功：`√`
- 失败：`X`，提示：`请上电或插上网线`

#### 强制退出规则
若检测项 1 失败，则脚本直接结束，不继续执行后续检测。

---

### 5.2 检测项 2：MDC1A NAS 挂载检测（160盘）

#### 输入
- host: `192.168.30.41`
- nas_ip: `192.168.79.160`

#### 检测逻辑
1. SSH 登录 MDC1A
2. 执行 `df -h`
3. 检测输出是否包含 NAS IP（`192.168.79.160`）
4. 提取可用容量（df 第 4 列 `Avail`）并转换为 GB
5. 判断可用容量必须 >= 800GB
6. 判断挂载点可真实访问（避免 stale 假挂）：
   - `ls /mnt/share`
   - `touch` + `rm` 写入测试

#### 自动修复逻辑（重要）
如果挂载不可用或容量不足，必须执行一次自动修复：

- `umount -l /mnt/share`
- `timeout 8s mount -t cifs //nas_ip/nas /mnt/share -o <mount_opts>`

只允许执行一次修复，不允许循环重试。

#### 输出要求
- 成功：提示 `可用容量 <avail>`
- 失败：提示换盘，例如：
  - `挂载失败或盘不可用（已自动清理并重挂一次），请换盘。`
  - `盘状态异常，请换盘。`
  - `可用容量 <avail>（<800G），请换盘。`

---

### 5.3 检测项 3：MDC2 NAS 挂载检测（60盘）

与检测项 2 完全相同，区别：

- host: `192.168.30.143`
- nas_ip: `192.168.79.60`

---

### 5.4 检测项 4-9：MDC1A Topic 发布检测

#### 输入
- host: `192.168.30.41`
- MDC1_TOPIC_CMDS topic 命令列表

#### 并发执行
- 最大并发 worker = 2

#### 输出解析规则（windows 判定）
`pmupload` 输出中仅解析满足条件的行：

- 行首以 `/` 开头
- 行末为整数（windows 值）

提取为数组，例如：

```text
windows=[1,2,3]
```

#### 判定规则
- windows 为空 → FAIL
- windows 全为 0 → FAIL
- windows 任意为 0 → FAIL
- windows 全部 > 0 → OK

#### 双重执行逻辑
每条 topic 检测最多执行两次：

1. `get_pty=False`
2. 若失败或全 0，则执行 `get_pty=True`

#### 特殊提示规则
若检测命令包含 `/lidar_side_front` 且失败，则提示前缀必须加：

```text
请驾驶员挂D档并踩住刹车，...
```

---

### 5.5 检测项 10-13：MDC2 Topic 发布检测

逻辑同 MDC1A Topic 检测，但：

- host = `192.168.30.143`
- 最大并发 worker = 4

---

## 6. 总体成功条件

当且仅当以下全部满足，脚本才判定成功：

- 车机状态 OK
- MDC1A 挂载 OK
- MDC2 挂载 OK
- MDC1A Topics 全部 OK
- MDC2 Topics 全部 OK

成功时输出：

```text
车辆正常，可以正常采集驾驶信息。
```

并退出。

---

## 7. 失败项复检功能（交互要求）

### 7.1 用户输入命令

当全量检测失败后，必须提示：

- `R`：重启全量检测
- `X`：只检测失败项
- `Q`：退出脚本

### 7.2 failed-only 检测逻辑

- 从上一次检测表格中筛选失败项（状态包含 X）
- 仅对失败项重新检测
- 输出 failed-only 表格结果

### 7.3 循环逻辑

如果 failed-only 检测仍失败，则继续提示：

- `R`：全量重检
- `X`：继续只检测失败项
- `Q`：退出

若用户选择 `X`，则使用本次 failed-only 输出作为下一轮失败项基准。

---

## 8. 非功能需求

### 8.1 稳定性要求
- 所有 SSH 命令必须设置 timeout，防止卡死
- mount 必须使用 `timeout` 包裹
- 挂载失败只允许自动修复一次，禁止无限循环重试

### 8.2 并发控制
- pmupload 检测必须限制并发（防止输出污染）

### 8.3 兼容性
- Windows 清屏使用 `cls`
- Linux 清屏使用 ANSI `\033c`
- Windows 按键读取使用 `msvcrt.getch()`
- Linux 按键读取使用 `input()`（需要回车）

### 8.4 可读性要求
- 输出必须是对齐表格
- 中文宽度必须正确对齐

---

## 9. 已知风险与限制

1. root 密码、NAS 密码明文写死，存在安全风险。
2. 依赖 `paramiko`，目标环境必须安装该库。
3. `pmupload` 输出格式必须稳定，否则 windows 解析会失败。
4. `df -h` 输出格式依赖字段顺序（Avail 在第 4 列）。

---

## 10. 交付物

- `check.py` 脚本文件
- 支持全量检测 + 失败项复检
- 输出标准表格
- 成功时提示可采集驾驶信息
