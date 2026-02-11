// check.cpp - 车辆采集驾驶数据前环境健康检查工具（C++版本）
// 依赖: libssh2, OpenSSL
// 编译说明:
//   静态编译: g++ -std=c++17 -O2 -static -o check_cpp check.cpp -lssh2 -lssl -lcrypto -lpthread -ldl -lz

#include <iostream>
#include <string>
#include <vector>
#include <map>
#include <set>
#include <regex>
#include <thread>
#include <mutex>
#include <future>
#include <chrono>
#include <sstream>
#include <algorithm>
#include <cstring>
#include <cstdlib>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <unistd.h>
#include <fcntl.h>
#include <termios.h>
#include <libssh2.h>

// ===== 固定配置 =====
const std::vector<std::string> HOSTS = {"192.168.30.143", "192.168.30.41", "192.168.30.43"};

const std::string USERNAME = "root";
const std::string PASSWORD = "Abcd12#$";
const int PORT = 22;

const std::string NAS_USER = "admin123";
const std::string NAS_PASS = "Huawei123";

const int CONNECT_TIMEOUT = 8;
const int CMD_TIMEOUT = 8;
const int PMUPLOAD_TIMEOUT = 20;
const int MOUNT_TIMEOUT_SEC = 8;

const std::string MDC1_IP = "192.168.30.41";
const std::string MDC2_IP = "192.168.30.143";

const std::string NAS_160 = "192.168.79.160";
const std::string NAS_60 = "192.168.79.60";

const std::string MOUNT_POINT = "/mnt/share";
const double MIN_AVAIL_GB = 800.0;

const int MDC1_MAX_WORKERS = 2;
const int MDC2_MAX_WORKERS = 4;

// Topic 映射
struct TopicCmd {
    std::string name;
    std::string cmd;
};

const std::vector<TopicCmd> MDC1_TOPIC_CMDS = {
    {"4. MDC1A 左侧 DTOF", "timeout 8s pmupload adstopic hz /dtof_left"},
    {"5. MDC1A 右侧 DTOF", "timeout 8s pmupload adstopic hz /dtof_right"},
    {"6. MDC1A 后向 DTOF", "timeout 8s pmupload adstopic hz /dtof_rear"},
    {"7. MDC1A 感知目标列表", "timeout 8s pmupload adstopic hz /object_array"},
    {"8. MDC1A 融合感知目标列表", "timeout 8s pmupload adstopic hz /object_array_fusion"},
    {"9. MDC1A 前向激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_front"},
};

const std::vector<TopicCmd> MDC2_TOPIC_CMDS = {
    {"10. MDC2 后向激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_rear"},
    {"11. MDC2 右侧激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_right"},
    {"12. MDC2 车顶激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_roof"},
    {"13. MDC2 左侧激光雷达", "timeout 8s pmupload adstopic hz /lidar_side_left"},
};

// ANSI colors
const std::string GREEN = "\033[92m";
const std::string RED = "\033[91m";
const std::string RESET = "\033[0m";
const std::string OK_STATUS = GREEN + "√" + RESET;
const std::string FAIL_STATUS = RED + "X" + RESET;

// Row 表示检测结果行
struct Row {
    std::string item;
    std::string status;
    std::string tip;
};

// ---------- 辅助函数 ----------
std::string stripAnsi(const std::string& s) {
    static std::regex ansi_re("\x1b\\[[0-9;]*m");
    return std::regex_replace(s, ansi_re, "");
}

bool isWideChar(wchar_t ch) {
    return (ch >= 0x1100 && ch <= 0x115F) ||
           (ch >= 0x2E80 && ch <= 0x9FFF) ||
           (ch >= 0xAC00 && ch <= 0xD7AF) ||
           (ch >= 0xF900 && ch <= 0xFAFF) ||
           (ch >= 0xFE10 && ch <= 0xFE1F) ||
           (ch >= 0xFE30 && ch <= 0xFE6F) ||
           (ch >= 0xFF00 && ch <= 0xFF60) ||
           (ch >= 0xFFE0 && ch <= 0xFFE6);
}

int visualWidth(const std::string& s) {
    std::string cleaned = stripAnsi(s);
    int w = 0;
    const char* ptr = cleaned.c_str();
    const char* end = ptr + cleaned.size();
    
    while (ptr < end) {
        unsigned char c = static_cast<unsigned char>(*ptr);
        if (c < 0x80) {
            w += 1;
            ptr++;
        } else if ((c & 0xE0) == 0xC0) {
            w += 1;
            ptr += 2;
        } else if ((c & 0xF0) == 0xE0) {
            // UTF-8 3字节字符，通常是CJK
            w += 2;
            ptr += 3;
        } else if ((c & 0xF8) == 0xF0) {
            w += 2;
            ptr += 4;
        } else {
            w += 1;
            ptr++;
        }
    }
    return w;
}

std::string padLeft(const std::string& s, int width) {
    int w = visualWidth(s);
    if (w >= width) return s;
    return s + std::string(width - w, ' ');
}

void printTable(const std::vector<Row>& rows) {
    Row header = {"检测项", "状态", "提醒"};
    int w1 = visualWidth(header.item);
    int w2 = visualWidth(header.status);
    int w3 = visualWidth(header.tip);
    
    for (const auto& r : rows) {
        w1 = std::max(w1, visualWidth(r.item));
        w2 = std::max(w2, visualWidth(r.status));
        w3 = std::max(w3, visualWidth(r.tip));
    }
    
    std::cout << padLeft(header.item, w1) << "  " 
              << padLeft(header.status, w2) << "  " 
              << padLeft(header.tip, w3) << "\n";
    std::cout << std::string(w1, '-') << "  " 
              << std::string(w2, '-') << "  " 
              << std::string(w3, '-') << "\n";
    
    for (const auto& r : rows) {
        std::cout << padLeft(r.item, w1) << "  " 
                  << padLeft(r.status, w2) << "  " 
                  << padLeft(r.tip, w3) << "\n";
    }
}

void clearScreen() {
    std::cout << "\033c" << std::flush;
}

std::string readKey() {
    std::string line;
    std::getline(std::cin, line);
    if (!line.empty()) {
        return std::string(1, std::tolower(line[0]));
    }
    return "";
}

// ---------- SSH helpers (使用 libssh2) ----------
class SSHClient {
public:
    SSHClient() : sock(-1), session(nullptr), channel(nullptr) {}
    
    ~SSHClient() {
        close();
    }
    
    bool connect(const std::string& host, int port, const std::string& user, const std::string& pass, int timeout) {
        sock = socket(AF_INET, SOCK_STREAM, 0);
        if (sock < 0) return false;
        
        // 设置超时
        struct timeval tv;
        tv.tv_sec = timeout;
        tv.tv_usec = 0;
        setsockopt(sock, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
        setsockopt(sock, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
        
        struct sockaddr_in addr;
        addr.sin_family = AF_INET;
        addr.sin_port = htons(port);
        inet_pton(AF_INET, host.c_str(), &addr.sin_addr);
        
        if (::connect(sock, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
            ::close(sock);
            sock = -1;
            return false;
        }
        
        session = libssh2_session_init();
        if (!session) {
            ::close(sock);
            sock = -1;
            return false;
        }
        
        libssh2_session_set_timeout(session, timeout * 1000);
        
        if (libssh2_session_handshake(session, sock) != 0) {
            libssh2_session_free(session);
            session = nullptr;
            ::close(sock);
            sock = -1;
            return false;
        }
        
        if (libssh2_userauth_password(session, user.c_str(), pass.c_str()) != 0) {
            libssh2_session_disconnect(session, "Auth failed");
            libssh2_session_free(session);
            session = nullptr;
            ::close(sock);
            sock = -1;
            return false;
        }
        
        return true;
    }
    
    std::tuple<int, std::string, std::string> execCmd(const std::string& cmd, int timeout) {
        if (!session) return std::make_tuple(-1, "", "");
        
        LIBSSH2_CHANNEL* ch = libssh2_channel_open_session(session);
        if (!ch) return std::make_tuple(-1, "", "");
        
        if (libssh2_channel_exec(ch, cmd.c_str()) != 0) {
            libssh2_channel_free(ch);
            return std::make_tuple(-1, "", "");
        }
        
        std::string out, err;
        char buffer[4096];
        
        auto start = std::chrono::steady_clock::now();
        while (true) {
            auto elapsed = std::chrono::duration_cast<std::chrono::seconds>(
                std::chrono::steady_clock::now() - start).count();
            if (elapsed > timeout) break;
            
            int rc = libssh2_channel_read(ch, buffer, sizeof(buffer) - 1);
            if (rc > 0) {
                buffer[rc] = '\0';
                out += buffer;
            } else if (rc == LIBSSH2_ERROR_EAGAIN) {
                std::this_thread::sleep_for(std::chrono::milliseconds(10));
            } else {
                break;
            }
        }
        
        // 读取 stderr
        while (true) {
            int rc = libssh2_channel_read_stderr(ch, buffer, sizeof(buffer) - 1);
            if (rc > 0) {
                buffer[rc] = '\0';
                err += buffer;
            } else {
                break;
            }
        }
        
        libssh2_channel_send_eof(ch);
        libssh2_channel_wait_eof(ch);
        libssh2_channel_wait_closed(ch);
        
        int exitCode = libssh2_channel_get_exit_status(ch);
        libssh2_channel_free(ch);
        
        return std::make_tuple(exitCode, out, err);
    }
    
    void close() {
        if (channel) {
            libssh2_channel_free(channel);
            channel = nullptr;
        }
        if (session) {
            libssh2_session_disconnect(session, "Normal shutdown");
            libssh2_session_free(session);
            session = nullptr;
        }
        if (sock >= 0) {
            ::close(sock);
            sock = -1;
        }
    }
    
private:
    int sock;
    LIBSSH2_SESSION* session;
    LIBSSH2_CHANNEL* channel;
};

bool trySSH(const std::string& host) {
    SSHClient client;
    bool ok = client.connect(host, PORT, USERNAME, PASSWORD, CONNECT_TIMEOUT);
    client.close();
    return ok;
}

// ---------- df / mount ----------
std::pair<double, bool> parseSizeToGB(const std::string& sizeStr) {
    static std::regex re("^([0-9]*\\.?[0-9]+)\\s*([KMGTP])([iI]?)B?$");
    std::smatch m;
    std::string s = sizeStr;
    // trim
    s.erase(0, s.find_first_not_of(" \t\n\r"));
    s.erase(s.find_last_not_of(" \t\n\r") + 1);
    
    if (!std::regex_match(s, m, re)) {
        return std::make_pair(0.0, false);
    }
    
    double val = std::stod(m[1].str());
    char unit = std::toupper(m[2].str()[0]);
    
    std::map<char, double> factors = {
        {'K', 1.0 / (1024 * 1024)},
        {'M', 1.0 / 1024},
        {'G', 1.0},
        {'T', 1024.0},
        {'P', 1024.0 * 1024.0}
    };
    
    return std::make_pair(val * factors[unit], true);
}

std::tuple<bool, std::string, double, bool> dfFindMountAvail(const std::string& dfOut, const std::string& nasIP) {
    std::istringstream iss(dfOut);
    std::string line;
    
    while (std::getline(iss, line)) {
        if (line.find(nasIP) != std::string::npos) {
            std::istringstream lineStream(line);
            std::vector<std::string> parts;
            std::string part;
            while (lineStream >> part) {
                parts.push_back(part);
            }
            
            if (parts.size() >= 6) {
                std::string availStr = parts[3];
                auto [availGB, ok] = parseSizeToGB(availStr);
                return std::make_tuple(true, availStr, availGB, ok);
            }
            return std::make_tuple(true, "", 0.0, false);
        }
    }
    return std::make_tuple(false, "", 0.0, false);
}

std::string buildMountCmd(const std::string& nasIP) {
    std::ostringstream oss;
    oss << "mkdir -p " << MOUNT_POINT << "; "
        << "umount -l " << MOUNT_POINT << " 2>/dev/null || true; "
        << "timeout " << MOUNT_TIMEOUT_SEC << "s mount -t cifs //" << nasIP << "/nas " << MOUNT_POINT
        << " -o vers=2.0,username=" << NAS_USER << ",password=" << NAS_PASS << ",cache=strict,"
        << "uid=1000,forceuid,gid=1000,forcegid,"
        << "file_mode=0755,dir_mode=0755,soft,nounix,noserverino,mapposix,"
        << "rsize=65536,wsize=65536,bsize=1048576,echo_interval=60,actimeo=1";
    return oss.str();
}

bool checkMountAlive(SSHClient& client) {
    auto [code1, out1, err1] = client.execCmd("ls " + MOUNT_POINT + " >/dev/null 2>&1", CMD_TIMEOUT);
    if (code1 != 0) return false;
    
    auto [code2, out2, err2] = client.execCmd(
        "touch " + MOUNT_POINT + "/.__nas_test__ && rm -f " + MOUNT_POINT + "/.__nas_test__ >/dev/null 2>&1",
        CMD_TIMEOUT);
    return code2 == 0;
}

std::tuple<bool, std::string, bool> ensureMountAndGetDF(const std::string& host, const std::string& nasIP) {
    bool didMountAttempt = false;
    SSHClient client;
    
    if (!client.connect(host, PORT, USERNAME, PASSWORD, CONNECT_TIMEOUT)) {
        return std::make_tuple(false, "", didMountAttempt);
    }
    
    auto [code, dfOut, dfErr] = client.execCmd("df -h", CMD_TIMEOUT);
    auto [mounted, availStr, availGB, ok] = dfFindMountAvail(dfOut, nasIP);
    
    if (mounted && ok && availGB >= MIN_AVAIL_GB && checkMountAlive(client)) {
        client.close();
        return std::make_tuple(true, dfOut, false);
    }
    
    didMountAttempt = true;
    client.execCmd(buildMountCmd(nasIP), CMD_TIMEOUT);
    
    auto [code2, dfOut2, dfErr2] = client.execCmd("df -h", CMD_TIMEOUT);
    auto [mounted2, availStr2, availGB2, ok2] = dfFindMountAvail(dfOut2, nasIP);
    bool okResult = mounted2 && ok2 && availGB2 >= MIN_AVAIL_GB && checkMountAlive(client);
    
    client.close();
    return std::make_tuple(okResult, dfOut2, true);
}

std::pair<bool, Row> checkMountRowWithAutoMount(const std::string& item, const std::string& host,
                                                  const std::string& nasIP, const std::string& missingTip) {
    auto [mounted, dfOut, didTry] = ensureMountAndGetDF(host, nasIP);
    
    if (!mounted) {
        return std::make_pair(false, Row{item, FAIL_STATUS, "挂载失败或盘不可用（已自动清理并重挂一次），请换盘。"});
    }
    
    auto [m, availStr, availGB, ok] = dfFindMountAvail(dfOut, nasIP);
    if (!m || !ok || availStr.empty()) {
        return std::make_pair(false, Row{item, FAIL_STATUS, "盘状态异常，请换盘。"});
    }
    if (availGB < MIN_AVAIL_GB) {
        return std::make_pair(false, Row{item, FAIL_STATUS, "可用容量 " + availStr + "（<800G），请换盘。"});
    }
    
    return std::make_pair(true, Row{item, OK_STATUS, "可用容量 " + availStr});
}

// ---------- pmupload parsing ----------
std::vector<int> parsePmuploadWindows(const std::string& text) {
    static std::regex re("^\\s*/\\S+.*\\s(\\d+)\\s*$");
    std::vector<int> windows;
    std::istringstream iss(text);
    std::string line;
    
    while (std::getline(iss, line)) {
        std::string trimmed = line;
        trimmed.erase(0, trimmed.find_first_not_of(" \t"));
        if (trimmed.empty() || trimmed[0] != '/') continue;
        
        std::smatch m;
        if (std::regex_match(line, m, re)) {
            try {
                windows.push_back(std::stoi(m[1].str()));
            } catch (...) {}
        }
    }
    return windows;
}

bool allZero(const std::vector<int>& arr) {
    for (int v : arr) {
        if (v != 0) return false;
    }
    return true;
}

bool hasZero(const std::vector<int>& arr) {
    for (int v : arr) {
        if (v == 0) return true;
    }
    return false;
}

std::string vectorToString(const std::vector<int>& v) {
    std::ostringstream oss;
    oss << "[";
    for (size_t i = 0; i < v.size(); ++i) {
        if (i > 0) oss << ", ";
        oss << v[i];
    }
    oss << "]";
    return oss.str();
}

std::tuple<std::string, Row, bool> runPmuploadCheck(const std::string& host,
                                                      const std::string& itemName,
                                                      const std::string& cmd) {
    auto runOnce = [&]() -> std::vector<int> {
        SSHClient client;
        if (!client.connect(host, PORT, USERNAME, PASSWORD, CONNECT_TIMEOUT)) {
            return {};
        }
        
        auto [code, out, err] = client.execCmd(cmd, PMUPLOAD_TIMEOUT);
        client.close();
        
        std::string merged = out;
        if (!out.empty() && !err.empty()) merged += "\n";
        merged += err;
        
        if (merged.empty()) return {};
        return parsePmuploadWindows(merged);
    };
    
    std::vector<int> windows = runOnce();
    if (windows.empty() || allZero(windows)) {
        windows = runOnce();
    }
    
    std::string tipList = "windows=" + vectorToString(windows);
    
    auto addDriveTipPrefix = [&](const std::string& tip) -> std::string {
        if (cmd.find("/lidar_side_front") != std::string::npos) {
            return "请驾驶员挂D档并踩住刹车，" + tip;
        }
        return tip;
    };
    
    if (windows.empty()) {
        std::string tip = "可能并发过高/Topic未发布/跑错IP | " + cmd + " | " + tipList;
        return std::make_tuple(itemName, Row{itemName, FAIL_STATUS, addDriveTipPrefix(tip)}, false);
    }
    
    if (hasZero(windows)) {
        std::string tip = cmd + " | " + tipList;
        return std::make_tuple(itemName, Row{itemName, FAIL_STATUS, addDriveTipPrefix(tip)}, false);
    }
    
    return std::make_tuple(itemName, Row{itemName, OK_STATUS, tipList}, true);
}

std::pair<std::vector<Row>, bool> runPmuploadGroup(const std::string& host,
                                                     const std::vector<TopicCmd>& items,
                                                     int maxWorkers) {
    std::vector<std::string> orderedNames;
    for (const auto& item : items) {
        orderedNames.push_back(item.name);
    }
    
    std::map<std::string, Row> rowMap;
    std::map<std::string, bool> okMap;
    std::mutex mu;
    
    std::vector<std::future<void>> futures;
    std::counting_semaphore<100> sem(maxWorkers);
    
    for (const auto& item : items) {
        futures.push_back(std::async(std::launch::async, [&, name = item.name, cmd = item.cmd]() {
            sem.acquire();
            auto [n, row, ok] = runPmuploadCheck(host, name, cmd);
            {
                std::lock_guard<std::mutex> lock(mu);
                rowMap[n] = row;
                okMap[n] = ok;
            }
            sem.release();
        }));
    }
    
    for (auto& f : futures) {
        f.wait();
    }
    
    std::vector<Row> rows;
    bool allOK = true;
    for (const auto& name : orderedNames) {
        rows.push_back(rowMap[name]);
        if (!okMap[name]) allOK = false;
    }
    
    return std::make_pair(rows, allOK);
}

// ---------- failed-only (X) ----------
bool isFailStatus(const std::string& status) {
    std::string cleaned = stripAnsi(status);
    return cleaned.find('X') != std::string::npos;
}

std::set<std::string> filterFailedItems(const std::vector<Row>& rows) {
    std::set<std::string> failed;
    for (const auto& r : rows) {
        if (isFailStatus(r.status)) {
            failed.insert(r.item);
        }
    }
    return failed;
}

std::pair<bool, std::vector<Row>> runFullCheck() {
    std::cout << "开始检测...预计一分钟。" << std::endl;
    std::vector<Row> rows;
    
    bool carOK = true;
    for (const auto& h : HOSTS) {
        if (!trySSH(h)) {
            carOK = false;
            break;
        }
    }
    
    if (carOK) {
        rows.push_back(Row{"1. 车机状态", OK_STATUS, ""});
    } else {
        rows.push_back(Row{"1. 车机状态", FAIL_STATUS, "请上电或插上网线"});
        return std::make_pair(false, rows);
    }
    
    auto [mdc1OK, row2] = checkMountRowWithAutoMount(
        "2. " + MDC1_IP + " MDC1A", MDC1_IP, NAS_160, "未检测到 160 盘。");
    rows.push_back(row2);
    
    auto [mdc2OK, row3] = checkMountRowWithAutoMount(
        "3. " + MDC2_IP + " MDC2", MDC2_IP, NAS_60, "未检测到 60 盘。");
    rows.push_back(row3);
    
    auto [mdc1Rows, mdc1TopicsOK] = runPmuploadGroup(MDC1_IP, MDC1_TOPIC_CMDS, MDC1_MAX_WORKERS);
    rows.insert(rows.end(), mdc1Rows.begin(), mdc1Rows.end());
    
    auto [mdc2Rows, mdc2TopicsOK] = runPmuploadGroup(MDC2_IP, MDC2_TOPIC_CMDS, MDC2_MAX_WORKERS);
    rows.insert(rows.end(), mdc2Rows.begin(), mdc2Rows.end());
    
    bool allSuccess = carOK && mdc1OK && mdc2OK && mdc1TopicsOK && mdc2TopicsOK;
    return std::make_pair(allSuccess, rows);
}

std::pair<bool, std::vector<Row>> runFailedOnlyCheck(const std::vector<Row>& prevRows) {
    std::set<std::string> failed = filterFailedItems(prevRows);
    if (failed.empty()) {
        return std::make_pair(true, std::vector<Row>{});
    }
    
    if (failed.count("1. 车机状态")) {
        std::vector<Row> rows;
        bool carOK = true;
        for (const auto& h : HOSTS) {
            if (!trySSH(h)) {
                carOK = false;
                break;
            }
        }
        if (carOK) {
            rows.push_back(Row{"1. 车机状态", OK_STATUS, ""});
        } else {
            rows.push_back(Row{"1. 车机状态", FAIL_STATUS, "请上电或插上网线"});
        }
        return std::make_pair(carOK, rows);
    }
    
    std::vector<Row> rows;
    bool allOK = true;
    
    std::string item2 = "2. " + MDC1_IP + " MDC1A";
    if (failed.count(item2)) {
        auto [ok2, row2] = checkMountRowWithAutoMount(item2, MDC1_IP, NAS_160, "未检测到 160 盘。");
        rows.push_back(row2);
        allOK = allOK && ok2;
    }
    
    std::string item3 = "3. " + MDC2_IP + " MDC2";
    if (failed.count(item3)) {
        auto [ok3, row3] = checkMountRowWithAutoMount(item3, MDC2_IP, NAS_60, "未检测到 60 盘。");
        rows.push_back(row3);
        allOK = allOK && ok3;
    }
    
    auto runSelected = [&](const std::string& host, const std::vector<TopicCmd>& items, int maxWorkers) 
        -> std::pair<std::vector<Row>, bool> {
        std::vector<TopicCmd> selected;
        for (const auto& item : items) {
            if (failed.count(item.name)) {
                selected.push_back(item);
            }
        }
        if (selected.empty()) {
            return std::make_pair(std::vector<Row>{}, true);
        }
        return runPmuploadGroup(host, selected, maxWorkers);
    };
    
    auto [m1Rows, m1OK] = runSelected(MDC1_IP, MDC1_TOPIC_CMDS, MDC1_MAX_WORKERS);
    rows.insert(rows.end(), m1Rows.begin(), m1Rows.end());
    allOK = allOK && m1OK;
    
    auto [m2Rows, m2OK] = runSelected(MDC2_IP, MDC2_TOPIC_CMDS, MDC2_MAX_WORKERS);
    rows.insert(rows.end(), m2Rows.begin(), m2Rows.end());
    allOK = allOK && m2OK;
    
    return std::make_pair(allOK, rows);
}

int main() {
    // 初始化 libssh2
    if (libssh2_init(0) != 0) {
        std::cerr << "Failed to initialize libssh2" << std::endl;
        return 1;
    }
    
    std::vector<Row> lastRows;
    while (true) {
        clearScreen();
        auto [ok, currentRows] = runFullCheck();
        lastRows = currentRows;
        printTable(lastRows);
        
        if (ok) {
            std::cout << "车辆正常，可以正常采集驾驶信息。" << std::endl;
            break;
        }
        
        std::cout << "按 R 重启全量检测，按 X 只检测失败项，按 Q 退出。" << std::endl;
        while (true) {
            std::string k = readKey();
            if (k == "r") {
                break;
            }
            if (k == "q") {
                libssh2_exit();
                return 0;
            }
            if (k == "x") {
                clearScreen();
                std::cout << "开始检测失败项...预计一分钟。" << std::endl;
                auto [okFailed, rowsFailed] = runFailedOnlyCheck(lastRows);
                if (!rowsFailed.empty()) {
                    printTable(rowsFailed);
                } else {
                    std::cout << "无失败项需要复检。" << std::endl;
                }
                if (okFailed) {
                    std::cout << "车辆正常，可以正常采集驾驶信息。" << std::endl;
                    libssh2_exit();
                    return 0;
                }
                std::cout << "按 R 重启全量检测，按 X 继续只检测失败项，按 Q 退出。" << std::endl;
                while (true) {
                    std::string k2 = readKey();
                    if (k2 == "r" || k2 == "x" || k2 == "q") {
                        k = k2;
                        break;
                    }
                }
                if (k == "r") {
                    break;
                }
                if (k == "q") {
                    libssh2_exit();
                    return 0;
                }
                if (k == "x") {
                    lastRows = rowsFailed;
                    continue;
                }
            }
        }
    }
    
    libssh2_exit();
    return 0;
}
