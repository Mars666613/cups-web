FRONTEND_DIR := frontend
BINARY := bin/cups-web
# 取件终端（打印机旁那台树莓派）的程序。它【只给 linux 用】——
# 别把它打进主二进制，两者的部署位置完全不同。
KIOSK_BINARY := bin/cups-kiosk

# 版本号来源：
#   1. 外部环境变量 VERSION 显式指定（CI 里从 github.ref_name 注入 tag 名）
#   2. 本地回退到 `git describe --tags --always --dirty`，这样在 dev 分支上也能带出
#      形如 `v1.2.3-4-gabcdef1-dirty` 的调试友好版本
#   3. 再退一步（非 git 仓库 / git 不可用）留空字符串，让 main.Version 保持默认 "dev"
# 取到的值通过 -ldflags "-X main.Version=..." 注入到二进制（见 Issue #26）。
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -s -w
ifneq ($(strip $(VERSION)),)
LDFLAGS += -X main.Version=$(VERSION)
endif

.PHONY: all frontend build kiosk clean docker-build
all: frontend build

frontend:
	@echo "Building frontend (expects Bun)..."
	cd $(FRONTEND_DIR) && bun install || true
	cd $(FRONTEND_DIR) && bunx vite build || bun run build

build:
	@echo "Building Go binary (version=$(VERSION))..."
	go build -ldflags='$(LDFLAGS)' -o $(BINARY) ./cmd/server

# 交叉编译树莓派端的取件程序。arm64 对应 64 位系统，armv7 对应 32 位。
# 用 `uname -m` 在树莓派上确认：aarch64 → arm64，armv7l → armv7。
kiosk:
	@echo "Building kiosk agent (linux/arm64 + linux/armv7)..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags='$(LDFLAGS)' -o $(KIOSK_BINARY)-linux-arm64 ./cmd/kiosk
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags='$(LDFLAGS)' -o $(KIOSK_BINARY)-linux-armv7 ./cmd/kiosk

clean:
	rm -f $(BINARY) $(KIOSK_BINARY)-linux-arm64 $(KIOSK_BINARY)-linux-armv7

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t cups-web:latest .

