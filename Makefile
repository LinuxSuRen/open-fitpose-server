BINARY ?= fitpose-server
GOFLAGS ?= GOPROXY=https://goproxy.cn,direct

.PHONY: build test vet run mock clean onnx-build

## build: 默认构建（内置 mock 提供者，无 onnxruntime 依赖）
build:
	$(GOFLAGS) go build -o bin/$(BINARY) ./cmd/server

## test: 单元测试（含竞态检测）
test:
	$(GOFLAGS) go test -race ./...

## vet: 静态检查
vet:
	$(GOFLAGS) go vet ./...

## run: 本地运行（mock 提供者，打开 http://127.0.0.1:8080）
run:
	$(GOFLAGS) go run ./cmd/server --addr :8080

## onnx-build: 带 ONNX Runtime 支持构建（先执行 scripts/setup-onnx.sh 并 source 输出的环境变量）
onnx-build:
	$(GOFLAGS) go build -tags onnx -o bin/$(BINARY)-onnx ./cmd/server

clean:
	rm -rf bin
