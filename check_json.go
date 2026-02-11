// check_json.go - 车辆采集驾驶数据前环境健康检查工具（JSON输出版本）
// 用法:
//   ./check_json                    # 全量检测
//   ./check_json -items=1,2,3       # 只检测指定项
//   ./check_json -items=car         # 只检测车机
//   ./check_json -items=mount       # 只检测挂载
//   ./check_json -items=topic       # 只检测Topic
//   ./check_json -help              # 显示帮助
//
// 编译:
//   CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o check_json check_json.go

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

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
type TopicCmd struct {
	Name string
	Cmd  string
}

var MDC1_TOPIC_CMDS = []TopicCmd{
	{"MDC1A 左侧 DTOF", "timeout 8s pmupload adstopic hz /dtof_left"},
	{"MDC1A 右侧 DTOF", "timeout 8s pmupload adstopic hz /dtof_right"},
	{"MDC1A 后向 DTOF", "timeout 8s pmupload adstopic hz /dtof_rear"},
	{"MDC1A 感知目标列表", "timeout 8s pmupload adstopic hz /object_array"},
	{"MDC1A 融合感知目标列表", "timeout 8s pmupload adstopic hz /object_array_fusion"},
	{"MDC1A 前向激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_front"},
}

var MDC2_TOPIC_CMDS = []TopicCmd{
	{"MDC2 后向激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_rear"},
	{"MDC2 右侧激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_right"},
	{"MDC2 车顶激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_roof"},
	{"MDC2 左侧激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_left"},
}

// 检测项ID映射
var ITEM_MAP = map[int]string{
	1:  "car",
	2:  "mount_mdc1",
	3:  "mount_mdc2",
	4:  "topic_dtof_left",
	5:  "topic_dtof_right",
	6:  "topic_dtof_rear",
	7:  "topic_object_array",
	8:  "topic_object_array_fusion",
	9:  "topic_lidar_side_front",
	10: "topic_lidar_side_rear",
	11: "topic_lidar_side_right",
	12: "topic_lidar_side_roof",
	13: "topic_lidar_side_left",
}

var PMUPLOAD_WINDOW_RE = regexp.MustCompile(`^\s*/\S+.*\s(\d+)\s*$`)

// JSON 输出结构
type CheckResult struct {
	Timestamp   string                `json:"timestamp"`
	Success     bool                  `json:"success"`
	Duration    float64               `json:"duration_seconds"`
	Passed      map[string]ResultItem `json:"passed"`
	Failed      map[string]ResultItem `json:"failed"`
	FailedCount int                   `json:"failed_count"`
	TotalCount  int                   `json:"total_count"`
}

type ResultItem struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// 内部使用的检测结果
type internalResult struct {
	ID      int
	Name    string
	OK      bool
	Message string
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

func checkMountRow(id int, item, host, nasIP string) internalResult {
	mounted, dfOut, _ := ensureMountAndGetDF(host, nasIP)

	if !mounted {
		return internalResult{id, item, false, "挂载失败或盘不可用（已自动清理并重挂一次），请换盘。"}
	}

	m, availStr, availGB, ok := dfFindMountAvail(dfOut, nasIP)
	if !m || !ok || availStr == "" {
		return internalResult{id, item, false, "盘状态异常，请换盘。"}
	}
	if availGB < MIN_AVAIL_GB {
		return internalResult{id, item, false, fmt.Sprintf("可用容量 %s（<800G），请换盘。", availStr)}
	}

	return internalResult{id, item, true, fmt.Sprintf("可用容量 %s", availStr)}
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

func runPmuploadCheck(id int, host, itemName, cmd string) internalResult {
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
		return internalResult{id, itemName, false, addDriveTipPrefix(tip)}
	}

	if hasZero(windows) {
		tip := fmt.Sprintf("%s | %s", cmd, tipList)
		return internalResult{id, itemName, false, addDriveTipPrefix(tip)}
	}

	return internalResult{id, itemName, true, tipList}
}

func runPmuploadGroup(host string, items []TopicCmd, startID, maxWorkers int, selected map[int]bool) []internalResult {
	var results []internalResult
	var mu sync.Mutex
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for i, item := range items {
		id := startID + i
		if selected != nil && !selected[id] {
			continue
		}
		wg.Add(1)
		go func(id int, name, cmd string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := runPmuploadCheck(id, host, name, cmd)
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(id, item.Name, item.Cmd)
	}

	wg.Wait()
	return results
}

// ---------- 检测逻辑 ----------
func runCheck(selected map[int]bool) CheckResult {
	startTime := time.Now()
	var items []internalResult

	// 1. 车机状态（必须先检测，只有成功后才继续）
	carOK := true
	for _, h := range HOSTS {
		if !trySSH(h) {
			carOK = false
			break
		}
	}

	// 无论用户是否选择检测项1，都记录车机状态结果
	if selected == nil || selected[1] {
		if carOK {
			items = append(items, internalResult{1, "车机状态", true, ""})
		} else {
			items = append(items, internalResult{1, "车机状态", false, "请上电或插上网线"})
		}
	} else if !carOK {
		// 用户没选择检测项1，但车机连不上，也要显示失败原因
		items = append(items, internalResult{1, "车机状态", false, "请上电或插上网线（前置检测失败）"})
	}

	// 如果车机状态失败，直接返回，不继续后面的检测
	if !carOK {
		return buildResult(startTime, items)
	}

	// 2. MDC1A 挂载
	if selected == nil || selected[2] {
		row2 := checkMountRow(2, fmt.Sprintf("%s MDC1A", MDC1_IP), MDC1_IP, NAS_160)
		items = append(items, row2)
	}

	// 3. MDC2 挂载
	if selected == nil || selected[3] {
		row3 := checkMountRow(3, fmt.Sprintf("%s MDC2", MDC2_IP), MDC2_IP, NAS_60)
		items = append(items, row3)
	}

	// 4-9. MDC1 Topics
	mdc1Results := runPmuploadGroup(MDC1_IP, MDC1_TOPIC_CMDS, 4, MDC1_MAX_WORKERS, selected)
	items = append(items, mdc1Results...)

	// 10-13. MDC2 Topics
	mdc2Results := runPmuploadGroup(MDC2_IP, MDC2_TOPIC_CMDS, 10, MDC2_MAX_WORKERS, selected)
	items = append(items, mdc2Results...)

	return buildResult(startTime, items)
}

// buildResult 构建返回结果
func buildResult(startTime time.Time, items []internalResult) CheckResult {
	// 分类到 passed 和 failed
	passed := make(map[string]ResultItem)
	failed := make(map[string]ResultItem)

	for _, item := range items {
		key := strconv.Itoa(item.ID)
		if item.OK {
			passed[key] = ResultItem{Name: item.Name, Message: item.Message}
		} else {
			failed[key] = ResultItem{Name: item.Name, Message: item.Message}
		}
	}

	duration := time.Since(startTime).Seconds()

	return CheckResult{
		Timestamp:   time.Now().Format(time.RFC3339),
		Success:     len(failed) == 0,
		Duration:    duration,
		Passed:      passed,
		Failed:      failed,
		FailedCount: len(failed),
		TotalCount:  len(items),
	}
}

func parseItems(itemsStr string) map[int]bool {
	if itemsStr == "" {
		return nil // 全量检测
	}

	selected := make(map[int]bool)

	// 支持别名
	itemsStr = strings.ToLower(itemsStr)
	switch itemsStr {
	case "car":
		selected[1] = true
		return selected
	case "mount":
		selected[2] = true
		selected[3] = true
		return selected
	case "topic", "topics":
		for i := 4; i <= 13; i++ {
			selected[i] = true
		}
		return selected
	case "mdc1":
		selected[2] = true
		for i := 4; i <= 9; i++ {
			selected[i] = true
		}
		return selected
	case "mdc2":
		selected[3] = true
		for i := 10; i <= 13; i++ {
			selected[i] = true
		}
		return selected
	case "all":
		return nil
	}

	// 解析数字列表
	parts := strings.Split(itemsStr, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if id, err := strconv.Atoi(p); err == nil && id >= 1 && id <= 13 {
			selected[id] = true
		}
	}

	if len(selected) == 0 {
		return nil
	}
	return selected
}

func printHelp() {
	fmt.Println(`check_json - 车辆采集驾驶数据前环境健康检查工具（JSON输出版本）

用法:
  ./check_json                    # 全量检测（所有13项）
  ./check_json -items=1,2,3       # 只检测指定项（按ID）
  ./check_json -items=car         # 只检测车机状态（项1）
  ./check_json -items=mount       # 只检测挂载（项2,3）
  ./check_json -items=topic       # 只检测Topic（项4-13）
  ./check_json -items=mdc1        # 只检测MDC1相关（项2,4-9）
  ./check_json -items=mdc2        # 只检测MDC2相关（项3,10-13）
  ./check_json -items=all         # 全量检测

检测项ID:
  1   车机状态
  2   MDC1A (192.168.30.41) NAS挂载
  3   MDC2 (192.168.30.143) NAS挂载
  4   MDC1A 左侧 DTOF
  5   MDC1A 右侧 DTOF
  6   MDC1A 后向 DTOF
  7   MDC1A 感知目标列表
  8   MDC1A 融合感知目标列表
  9   MDC1A 前向激光雷达
  10  MDC2 后向激光雷达
  11  MDC2 右侧激光雷达
  12  MDC2 车顶激光雷达
  13  MDC2 左侧激光雷达

输出:
  JSON格式输出到stdout，包含:
  - timestamp: 检测时间
  - success: 是否全部通过
  - duration_seconds: 检测耗时
  - items: 检测结果列表
  - failed_count: 失败项数量
  - total_count: 总检测项数量`)
}

func main() {
	itemsFlag := flag.String("items", "", "要检测的项目，可以是ID列表(1,2,3)或别名(car,mount,topic,mdc1,mdc2,all)")
	helpFlag := flag.Bool("help", false, "显示帮助信息")
	flag.BoolVar(helpFlag, "h", false, "显示帮助信息")

	flag.Parse()

	if *helpFlag {
		printHelp()
		os.Exit(0)
	}

	selected := parseItems(*itemsFlag)
	result := runCheck(selected)

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "JSON序列化失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(jsonBytes))

	if result.Success {
		os.Exit(0)
	} else {
		os.Exit(1)
	}
}
