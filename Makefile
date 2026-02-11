# check_car 编译构建文件
# 用法:
#   make all       - 编译所有版本
#   make go        - 编译 Go 版本 (Linux + Windows)
#   make go-json   - 编译 Go JSON 版本 (Linux)
#   make cpp       - 编译 C++ 版本 (Linux)
#   make clean     - 清理编译产物

.PHONY: all go go-json cpp clean go-deps cpp-deps web

# 输出目录
BUILD_DIR := build

# Go 相关
GO_SRC := check.go
GO_JSON_SRC := check_json.go
GO_LINUX_OUT := $(BUILD_DIR)/check_linux
GO_WINDOWS_OUT := $(BUILD_DIR)/check.exe
GO_JSON_OUT := $(BUILD_DIR)/check_json

# C++ 相关
CPP_SRC := check.cpp
CPP_OUT := $(BUILD_DIR)/check_cpp

all: go go-json cpp

# 创建构建目录
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# ========== Go 版本 ==========
go: go-deps $(BUILD_DIR) $(GO_LINUX_OUT) $(GO_WINDOWS_OUT)

go-json: go-deps $(BUILD_DIR) $(GO_JSON_OUT)

go-deps:
	@echo "检查/安装 Go 依赖..."
	go mod init check_car 2>/dev/null || true
	go get golang.org/x/crypto/ssh

$(GO_LINUX_OUT): $(GO_SRC) | $(BUILD_DIR)
	@echo "编译 Go Linux 版本 (静态链接)..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(GO_LINUX_OUT) $(GO_SRC)
	@echo "生成: $(GO_LINUX_OUT)"

$(GO_WINDOWS_OUT): $(GO_SRC) | $(BUILD_DIR)
	@echo "编译 Go Windows 版本 (静态链接)..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o $(GO_WINDOWS_OUT) $(GO_SRC)
	@echo "生成: $(GO_WINDOWS_OUT)"

$(GO_JSON_OUT): $(GO_JSON_SRC) | $(BUILD_DIR)
	@echo "编译 Go JSON 版本 (静态链接)..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(GO_JSON_OUT) $(GO_JSON_SRC)
	@echo "生成: $(GO_JSON_OUT)"

# ========== C++ 版本 ==========
cpp: $(BUILD_DIR) $(CPP_OUT)

$(CPP_OUT): $(CPP_SRC) | $(BUILD_DIR)
	@echo "编译 C++ Linux 版本 (静态链接)..."
	@echo "注意: 需要安装 libssh2-dev, libssl-dev, zlib1g-dev"
	g++ -std=c++20 -O2 -static -o $(CPP_OUT) $(CPP_SRC) -lssh2 -lssl -lcrypto -lpthread -ldl -lz 2>/dev/null || \
	g++ -std=c++20 -O2 -o $(CPP_OUT) $(CPP_SRC) -lssh2 -lssl -lcrypto -lpthread
	@echo "生成: $(CPP_OUT)"

# 安装 C++ 依赖 (Debian/Ubuntu)
cpp-deps:
	@echo "安装 C++ 依赖 (需要 sudo)..."
	sudo apt-get update
	sudo apt-get install -y libssh2-1-dev libssl-dev zlib1g-dev

# ========== 清理 ==========
clean:
	rm -rf $(BUILD_DIR)
	rm -f go.mod go.sum
	@echo "清理完成"

# ========== Web 服务 ==========
web: go-json
	@echo "启动 Web 服务..."
	@echo "请先安装依赖: pip install -r web/requirements.txt"
	@echo "然后运行: cd web && python app.py --check-cmd=../build/check_json"

# ========== 帮助 ==========
help:
	@echo "check_car 编译构建系统"
	@echo ""
	@echo "用法:"
	@echo "  make all       - 编译所有版本 (Go Linux/Windows + Go JSON + C++ Linux)"
	@echo "  make go        - 编译 Go 交互版本 (Linux + Windows 静态可执行文件)"
	@echo "  make go-json   - 编译 Go JSON 版本 (Linux 静态可执行文件，用于 Web 后端)"
	@echo "  make cpp       - 编译 C++ 版本 (Linux 静态可执行文件)"
	@echo "  make go-deps   - 安装 Go 依赖"
	@echo "  make cpp-deps  - 安装 C++ 依赖 (需要 sudo)"
	@echo "  make web       - 编译并提示启动 Web 服务"
	@echo "  make clean     - 清理编译产物"
	@echo ""
	@echo "编译产物:"
	@echo "  build/check_linux  - Go Linux 交互版本"
	@echo "  build/check.exe    - Go Windows 交互版本"
	@echo "  build/check_json   - Go JSON 输出版本 (用于 Web)"
	@echo "  build/check_cpp    - C++ Linux 版本"
