// check.go - 车辆采集驾驶数据前环境健康检查工具（Go版本）
// 编译说明:
//   Linux 静态编译:    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o check_linux check.go
//   Windows 静态编译:  CGO_ENABLED=0 GOOS=windows go build -ldflags="-s -w" -o check.exe check.go

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
)

// ===== 固定配置 =====
var HOSTS = []string{"192.168.30.143", "192.168.30.41", "192.168.30.43"}

const (
	USERNAME = "root"
	PASSWORD = "Abcd12#$"
	PORT     = 22

	NAS_USER = "admin123"
	NAS_PASS = "Huawei123"

	CONNECT_TIMEOUT   = 8 * time.Second
	CMD_TIMEOUT       = 8 * time.Second
	PMUPLOAD_TIMEOUT  = 20 * time.Second
	MOUNT_TIMEOUT_SEC = 8

	MDC1_IP = "192.168.30.41"
	MDC2_IP = "192.168.30.143"

	NAS_160 = "192.168.79.160"
	NAS_60  = "192.168.79.60"

	MOUNT_POINT  = "/mnt/share"
	MIN_AVAIL_GB = 800.0

	MDC1_MAX_WORKERS = 2
	MDC2_MAX_WORKERS = 4
)

// Topic 映射
var MDC1_TOPIC_CMDS = []struct {
	Name string
	Cmd  string
}{
	{"4. MDC1A 左侧 DTOF", "timeout 8s pmupload adstopic hz /dtof_left"},
	{"5. MDC1A 右侧 DTOF", "timeout 8s pmupload adstopic hz /dtof_right"},
	{"6. MDC1A 后向 DTOF", "timeout 8s pmupload adstopic hz /dtof_rear"},
	{"7. MDC1A 感知目标列表", "timeout 8s pmupload adstopic hz /object_array"},
	{"8. MDC1A 融合感知目标列表", "timeout 8s pmupload adstopic hz /object_array_fusion"},
	{"9. MDC1A 前向激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_front"},
}

var MDC2_TOPIC_CMDS = []struct {
	Name string
	Cmd  string
}{
	{"10. MDC2 后向激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_rear"},
	{"11. MDC2 右侧激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_right"},
	{"12. MDC2 车顶激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_roof"},
	{"13. MDC2 左侧激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_left"},
}

// ANSI colors
const (
	GREEN = "\033[92m"
	RED   = "\033[91m"
	RESET = "\033[0m"
)

var (
	OK   = GREEN + "√" + RESET
	FAIL = RED + "X" + RESET
)

var ANSI_RE = regexp.MustCompile(`\x1b\[[0-9;]*m`)
var PMUPLOAD_WINDOW_RE = regexp.MustCompile(`^\s*/\S+.*\s(\d+)\s*$`)

// Row 表示检测结果行
type Row struct {
	Item   string
	Status string
	Tip    string
}

// ---------- 中文宽度对齐 ----------
func visualWidth(s string) int {
	s = ANSI_RE.ReplaceAllString(s, "")
	w := 0
	for _, ch := range s {
		if unicode.Is(unicode.Han, ch) || isFullWidth(ch) {
			w += 2
		} else {
			w += 1
		}
	}
	return w
}

func isFullWidth(r rune) bool {
	// 简化判断: CJK 字符范围
	return (r >= 0x1100 && r <= 0x115F) ||
		(r >= 0x2E80 && r <= 0x9FFF) ||
		(r >= 0xAC00 && r <= 0xD7AF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE1F) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6)
}

func padLeft(s string, width int) string {
	w := visualWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func printTable(rows []Row) {
	header := Row{"检测项", "状态", "提醒"}
	w1 := visualWidth(header.Item)
	w2 := visualWidth(header.Status)
	w3 := visualWidth(header.Tip)

	for _, r := range rows {
		if v := visualWidth(r.Item); v > w1 {
			w1 = v
		}
		if v := visualWidth(r.Status); v > w2 {
			w2 = v
		}
		if v := visualWidth(r.Tip); v > w3 {
			w3 = v
		}
	}

	fmt.Printf("%s  %s  %s\n", padLeft(header.Item, w1), padLeft(header.Status, w2), padLeft(header.Tip, w3))
	fmt.Printf("%s  %s  %s\n", strings.Repeat("-", w1), strings.Repeat("-", w2), strings.Repeat("-", w3))
	for _, r := range rows {
		fmt.Printf("%s  %s  %s\n", padLeft(r.Item, w1), padLeft(r.Status, w2), padLeft(r.Tip, w3))
	}
}

func clearScreen() {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		cmd.Run()
	} else {
		fmt.Print("\033c")
	}
}

func readKey() string {
	if runtime.GOOS == "windows" {
		// Windows 下简单处理
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))
		if len(input) > 0 {
			return string(input[0])
		}
		return ""
	}
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if len(input) > 0 {
		return string(input[0])
	}
	return ""
}

// ---------- SSH helpers ----------
func sshConnect(host string) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User: USERNAME,
		Auth: []ssh.AuthMethod{
			ssh.Password(PASSWORD),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         CONNECT_TIMEOUT,
	}

	addr := fmt.Sprintf("%s:%d", host, PORT)
	conn, err := net.DialTimeout("tcp", addr, CONNECT_TIMEOUT)
	if err != nil {
		return nil, err
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return ssh.NewClient(c, chans, reqs), nil
}

func trySSH(host string) bool {
	client, err := sshConnect(host)
	if err != nil {
		return false
	}
	client.Close()
	return true
}

func execCmd(client *ssh.Client, cmd string, timeout time.Duration) (int, string, string, error) {
	session, err := client.NewSession()
	if err != nil {
		return -1, "", "", err
	}
	defer session.Close()

	var stdout, stderr strings.Builder

	stdoutPipe, _ := session.StdoutPipe()
	stderrPipe, _ := session.StderrPipe()

	done := make(chan error, 1)

	go func() {
		done <- session.Run(cmd)
	}()

	go io.Copy(&stdout, stdoutPipe)
	go io.Copy(&stderr, stderrPipe)

	select {
	case err := <-done:
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				exitCode = exitErr.ExitStatus()
			} else {
				exitCode = -1
			}
		}
		return exitCode, stdout.String(), stderr.String(), nil
	case <-time.After(timeout):
		session.Signal(ssh.SIGTERM)
		return -1, stdout.String(), stderr.String(), fmt.Errorf("timeout")
	}
}

// ---------- df / mount ----------
func parseSizeToGB(sizeStr string) (float64, bool) {
	s := strings.TrimSpace(sizeStr)
	re := regexp.MustCompile(`^([0-9]*\.?[0-9]+)\s*([KMGTP])([iI]?)B?$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}

	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}

	unit := strings.ToUpper(m[2])
	factors := map[string]float64{
		"K": 1.0 / (1024 * 1024),
		"M": 1.0 / 1024,
		"G": 1.0,
		"T": 1024.0,
		"P": 1024.0 * 1024.0,
	}

	return val * factors[unit], true
}

func dfFindMountAvail(dfOut, nasIP string) (bool, string, float64, bool) {
	for _, line := range strings.Split(dfOut, "\n") {
		if strings.Contains(line, nasIP) {
			parts := strings.Fields(line)
			if len(parts) >= 6 {
				availStr := parts[3]
				availGB, ok := parseSizeToGB(availStr)
				return true, availStr, availGB, ok
			}
			return true, "", 0, false
		}
	}
	return false, "", 0, false
}

func buildMountCmd(nasIP string) string {
	mountOpts := fmt.Sprintf("vers=2.0,username=%s,password=%s,cache=strict,"+
		"uid=1000,forceuid,gid=1000,forcegid,"+
		"file_mode=0755,dir_mode=0755,soft,nounix,noserverino,mapposix,"+
		"rsize=65536,wsize=65536,bsize=1048576,echo_interval=60,actimeo=1",
		NAS_USER, NAS_PASS)

	return fmt.Sprintf("mkdir -p %s; umount -l %s 2>/dev/null || true; timeout %ds mount -t cifs //%s/nas %s -o %s",
		MOUNT_POINT, MOUNT_POINT, MOUNT_TIMEOUT_SEC, nasIP, MOUNT_POINT, mountOpts)
}

func checkMountAlive(client *ssh.Client) bool {
	code1, _, _, _ := execCmd(client, fmt.Sprintf("ls %s >/dev/null 2>&1", MOUNT_POINT), CMD_TIMEOUT)
	if code1 != 0 {
		return false
	}
	code2, _, _, _ := execCmd(client, fmt.Sprintf("touch %s/.__nas_test__ && rm -f %s/.__nas_test__ >/dev/null 2>&1", MOUNT_POINT, MOUNT_POINT), CMD_TIMEOUT)
	return code2 == 0
}

func ensureMountAndGetDF(host, nasIP string) (bool, string, bool) {
	didMountAttempt := false
	client, err := sshConnect(host)
	if err != nil {
		return false, "", didMountAttempt
	}
	defer client.Close()

	_, dfOut, _, _ := execCmd(client, "df -h", CMD_TIMEOUT)
	mounted, _, availGB, ok := dfFindMountAvail(dfOut, nasIP)
	if mounted && ok && availGB >= MIN_AVAIL_GB && checkMountAlive(client) {
		return true, dfOut, false
	}

	didMountAttempt = true
	execCmd(client, buildMountCmd(nasIP), CMD_TIMEOUT)

	_, dfOut2, _, _ := execCmd(client, "df -h", CMD_TIMEOUT)
	mounted2, _, availGB2, ok2 := dfFindMountAvail(dfOut2, nasIP)
	okResult := mounted2 && ok2 && availGB2 >= MIN_AVAIL_GB && checkMountAlive(client)
	return okResult, dfOut2, true
}

func checkMountRowWithAutoMount(item, host, nasIP, missingTip string) (bool, Row) {
	mounted, dfOut, _ := ensureMountAndGetDF(host, nasIP)

	if !mounted {
		return false, Row{item, FAIL, "挂载失败或盘不可用（已自动清理并重挂一次），请换盘。"}
	}

	m, availStr, availGB, ok := dfFindMountAvail(dfOut, nasIP)
	if !m || !ok || availStr == "" {
		return false, Row{item, FAIL, "盘状态异常，请换盘。"}
	}
	if availGB < MIN_AVAIL_GB {
		return false, Row{item, FAIL, fmt.Sprintf("可用容量 %s（<800G），请换盘。", availStr)}
	}

	return true, Row{item, OK, fmt.Sprintf("可用容量 %s", availStr)}
}

// ---------- pmupload parsing ----------
func parsePmuploadWindows(text string) []int {
	var windows []int
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(strings.TrimLeft(line, " \t"), "/") {
			continue
		}
		m := PMUPLOAD_WINDOW_RE.FindStringSubmatch(line)
		if m != nil {
			if val, err := strconv.Atoi(m[1]); err == nil {
				windows = append(windows, val)
			}
		}
	}
	return windows
}

func runPmuploadCheck(host, itemName, cmd string) (string, Row, bool) {
	runOnce := func() []int {
		client, err := sshConnect(host)
		if err != nil {
			return nil
		}
		defer client.Close()

		_, out, errOut, _ := execCmd(client, cmd, PMUPLOAD_TIMEOUT)
		merged := out
		if out != "" && errOut != "" {
			merged += "\n"
		}
		merged += errOut
		if strings.TrimSpace(merged) == "" {
			return nil
		}
		return parsePmuploadWindows(merged)
	}

	windows := runOnce()
	if len(windows) == 0 || allZero(windows) {
		windows = runOnce()
	}

	tipList := fmt.Sprintf("windows=%v", windows)

	addDriveTipPrefix := func(tip string) string {
		if strings.Contains(cmd, "/lidar_side_front") {
			return "请驾驶员挂D档并踩住刹车，" + tip
		}
		return tip
	}

	if len(windows) == 0 {
		tip := fmt.Sprintf("可能并发过高/Topic未发布/跑错IP | %s | %s", cmd, tipList)
		return itemName, Row{itemName, FAIL, addDriveTipPrefix(tip)}, false
	}

	if hasZero(windows) {
		tip := fmt.Sprintf("%s | %s", cmd, tipList)
		return itemName, Row{itemName, FAIL, addDriveTipPrefix(tip)}, false
	}

	return itemName, Row{itemName, OK, tipList}, true
}

func allZero(arr []int) bool {
	for _, v := range arr {
		if v != 0 {
			return false
		}
	}
	return true
}

func hasZero(arr []int) bool {
	for _, v := range arr {
		if v == 0 {
			return true
		}
	}
	return false
}

type pmResult struct {
	Name string
	Row  Row
	OK   bool
}

func runPmuploadGroup(host string, items []struct{ Name, Cmd string }, maxWorkers int) ([]Row, bool) {
	orderedNames := make([]string, len(items))
	for i, item := range items {
		orderedNames[i] = item.Name
	}

	rowMap := make(map[string]Row)
	okMap := make(map[string]bool)
	var mu sync.Mutex

	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for _, item := range items {
		wg.Add(1)
		go func(name, cmd string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			n, row, ok := runPmuploadCheck(host, name, cmd)
			mu.Lock()
			rowMap[n] = row
			okMap[n] = ok
			mu.Unlock()
		}(item.Name, item.Cmd)
	}

	wg.Wait()

	rows := make([]Row, len(orderedNames))
	allOK := true
	for i, name := range orderedNames {
		rows[i] = rowMap[name]
		if !okMap[name] {
			allOK = false
		}
	}

	return rows, allOK
}

// ---------- failed-only (X) ----------
func isFailStatus(status string) bool {
	cleaned := ANSI_RE.ReplaceAllString(status, "")
	return strings.Contains(cleaned, "X")
}

func filterFailedItems(rows []Row) map[string]bool {
	failed := make(map[string]bool)
	for _, r := range rows {
		if isFailStatus(r.Status) {
			failed[r.Item] = true
		}
	}
	return failed
}

func runFullCheck() (bool, []Row) {
	fmt.Println("开始检测...预计一分钟。")
	var rows []Row

	carOK := true
	for _, h := range HOSTS {
		if !trySSH(h) {
			carOK = false
			break
		}
	}
	if carOK {
		rows = append(rows, Row{"1. 车机状态", OK, ""})
	} else {
		rows = append(rows, Row{"1. 车机状态", FAIL, "请上电或插上网线"})
		return false, rows
	}

	mdc1OK, row2 := checkMountRowWithAutoMount(
		fmt.Sprintf("2. %s MDC1A", MDC1_IP),
		MDC1_IP, NAS_160, "未检测到 160 盘。",
	)
	rows = append(rows, row2)

	mdc2OK, row3 := checkMountRowWithAutoMount(
		fmt.Sprintf("3. %s MDC2", MDC2_IP),
		MDC2_IP, NAS_60, "未检测到 60 盘。",
	)
	rows = append(rows, row3)

	mdc1Rows, mdc1TopicsOK := runPmuploadGroup(MDC1_IP, MDC1_TOPIC_CMDS, MDC1_MAX_WORKERS)
	rows = append(rows, mdc1Rows...)

	mdc2Rows, mdc2TopicsOK := runPmuploadGroup(MDC2_IP, MDC2_TOPIC_CMDS, MDC2_MAX_WORKERS)
	rows = append(rows, mdc2Rows...)

	allSuccess := carOK && mdc1OK && mdc2OK && mdc1TopicsOK && mdc2TopicsOK
	return allSuccess, rows
}

func runFailedOnlyCheck(prevRows []Row) (bool, []Row) {
	failed := filterFailedItems(prevRows)
	if len(failed) == 0 {
		return true, nil
	}

	if failed["1. 车机状态"] {
		var rows []Row
		carOK := true
		for _, h := range HOSTS {
			if !trySSH(h) {
				carOK = false
				break
			}
		}
		if carOK {
			rows = append(rows, Row{"1. 车机状态", OK, ""})
		} else {
			rows = append(rows, Row{"1. 车机状态", FAIL, "请上电或插上网线"})
		}
		return carOK, rows
	}

	var rows []Row
	allOK := true

	item2 := fmt.Sprintf("2. %s MDC1A", MDC1_IP)
	if failed[item2] {
		ok2, row2 := checkMountRowWithAutoMount(item2, MDC1_IP, NAS_160, "未检测到 160 盘。")
		rows = append(rows, row2)
		allOK = allOK && ok2
	}

	item3 := fmt.Sprintf("3. %s MDC2", MDC2_IP)
	if failed[item3] {
		ok3, row3 := checkMountRowWithAutoMount(item3, MDC2_IP, NAS_60, "未检测到 60 盘。")
		rows = append(rows, row3)
		allOK = allOK && ok3
	}

	runSelected := func(host string, items []struct{ Name, Cmd string }, maxWorkers int) ([]Row, bool) {
		var selected []struct{ Name, Cmd string }
		for _, item := range items {
			if failed[item.Name] {
				selected = append(selected, item)
			}
		}
		if len(selected) == 0 {
			return nil, true
		}

		orderedNames := make([]string, len(selected))
		for i, item := range selected {
			orderedNames[i] = item.Name
		}

		rowMap := make(map[string]Row)
		okMap := make(map[string]bool)
		var mu sync.Mutex

		sem := make(chan struct{}, maxWorkers)
		var wg sync.WaitGroup

		for _, item := range selected {
			wg.Add(1)
			go func(name, cmd string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				n, row, ok := runPmuploadCheck(host, name, cmd)
				mu.Lock()
				rowMap[n] = row
				okMap[n] = ok
				mu.Unlock()
			}(item.Name, item.Cmd)
		}

		wg.Wait()

		result := make([]Row, len(orderedNames))
		resultOK := true
		for i, name := range orderedNames {
			result[i] = rowMap[name]
			if !okMap[name] {
				resultOK = false
			}
		}
		return result, resultOK
	}

	m1Rows, m1OK := runSelected(MDC1_IP, MDC1_TOPIC_CMDS, MDC1_MAX_WORKERS)
	rows = append(rows, m1Rows...)
	allOK = allOK && m1OK

	m2Rows, m2OK := runSelected(MDC2_IP, MDC2_TOPIC_CMDS, MDC2_MAX_WORKERS)
	rows = append(rows, m2Rows...)
	allOK = allOK && m2OK

	return allOK, rows
}

func main() {
	var lastRows []Row
	for {
		clearScreen()
		ok, lastRows := runFullCheck()
		printTable(lastRows)

		if ok {
			fmt.Println("车辆正常，可以正常采集驾驶信息。")
			return
		}

		fmt.Println("按 R 重启全量检测，按 X 只检测失败项，按 Q 退出。")
		for {
			k := readKey()
			if k == "r" {
				break
			}
			if k == "q" {
				return
			}
			if k == "x" {
				clearScreen()
				fmt.Println("开始检测失败项...预计一分钟。")
				okFailed, rowsFailed := runFailedOnlyCheck(lastRows)
				if len(rowsFailed) > 0 {
					printTable(rowsFailed)
				} else {
					fmt.Println("无失败项需要复检。")
				}
				if okFailed {
					fmt.Println("车辆正常，可以正常采集驾驶信息。")
					return
				}
				fmt.Println("按 R 重启全量检测，按 X 继续只检测失败项，按 Q 退出。")
				for {
					k2 := readKey()
					if k2 == "r" || k2 == "x" || k2 == "q" {
						k = k2
						break
					}
				}
				if k == "r" {
					break
				}
				if k == "q" {
					return
				}
				if k == "x" {
					lastRows = rowsFailed
					continue
				}
			}
		}
	}
	_ = lastRows
}
