# Open FitPose Server

[![build](https://github.com/linuxsuren/open-fitpose-server/actions/workflows/build.yml/badge.svg)](https://github.com/linuxsuren/open-fitpose-server/actions/workflows/build.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

开源健身/运动姿势视觉识别服务端：接收摄像头画面，实时输出人体关键点、
动作计数（深蹲/俯卧撑/开合跳）与姿态纠错提示，供 Web、
[Android](https://github.com/linuxsuren/open-fitpose-android)、
[鸿蒙](https://github.com/linuxsuren/open-fitpose-ohos) 客户端接入。

## 架构

```
┌──────────────────┐        ┌─────────────────────┐        ┌───────────────────┐
│ open-fitpose-ohos│        │ open-fitpose-server │        │open-fitpose-android│
│ 鸿蒙 App (ArkTS) │ ─WS───▶│  Go 服务端           │◀──WS── │ Android App(Kotlin)│
└──────────────────┘        │  ONNX 姿态推理       │        └───────────────────┘
                            │  动作计数/纠错        │
                            │  内嵌 Web 前端       │◀── 浏览器直接访问
                            └─────────────────────┘
```

- **流式识别**：WebSocket 逐帧交互，服务端只处理最新帧（内置背压丢弃），
  携带应用层心跳与断线重连约定，见 [docs/protocol.md](docs/protocol.md)。
- **动作按需选择**：客户端通过 `select` 消息选择要计数的动作
  （深蹲/俯卧撑/开合跳），服务端只分析并下发所选动作，Web 前端提供切换按钮。
- **推理可插拔**：`Provider` 接口 + 两种实现——`mock`（零依赖、确定性输出，
  开发/演示/测试）与 `onnx`（ONNX Runtime 真实推理，`-tags onnx` 构建）。
- **模型自适应**：输入输出名、输入尺寸、NCHW/NHWC 布局自动从模型元数据探测；
  MoveNet 布局自动采用 0..255 像素输入与归一化坐标输出。
- **动作分析纯 Go**：关节角计算、三动作状态机计数、提示 code，
  全量单元测试覆盖，不依赖推理后端。
- **展示与数据分离**：服务端只下发稳定 code，文案由各端本地映射。

## 快速开始（mock 模式，零依赖）

```bash
go run ./cmd/server
# 打开 http://127.0.0.1:8080 → 开启摄像头 → 连接服务
# 页面会显示骨架叠加、三类动作计数与姿态提示
```

单帧冒烟（也可用 multipart：`curl -F frame=@x.jpg ...`）：

```bash
curl -s -X POST --data-binary @photo.jpg http://127.0.0.1:8080/api/v1/pose/detect
```

Docker：

```bash
docker build -t fitpose-server .
docker run -p 8080:8080 fitpose-server
```

## ONNX 部署（真实推理）

1. 准备 onnxruntime 动态库：

   ```bash
   bash scripts/setup-onnx.sh       # 下载并生成 env.sh
   source .cache/onnxruntime/env.sh
   ```

2. 准备姿态模型（COCO 17 点、输出 17×3），放到 `models/pose-model.onnx`。
   推荐从 [PINTO_model_zoo](https://github.com/PINTO0309/PINTO_model_zoo) 下载 MoveNet：

   ```bash
   curl -fL "https://s3.ap-northeast-2.wasabisys.com/pinto-model-zoo/115_MoveNet/lightning_v4/resources.tar.gz" -o /tmp/mn.tgz
   tar -xzf /tmp/mn.tgz -C /tmp saved_model/model_float32.onnx
   mkdir -p models && cp /tmp/saved_model/model_float32.onnx models/pose-model.onnx
   ```

3. 构建并启动（输入输出名/尺寸/布局自动探测，一般无需额外参数）：

   ```bash
   go build -tags onnx -o bin/fitpose-server-onnx ./cmd/server
   ./bin/fitpose-server-onnx --provider onnx --model models/pose-model.onnx
   ```

   动态库不在默认搜索路径时用 `--ort-lib` 指定；非 MoveNet 模型可用
   `--keypoint-layout xy`、`--input-layout nchw`、`--normalize-input` 适配。

## 配置项

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--addr` | `:8080` | HTTP 监听地址 |
| `--provider` | `mock` | `mock` 或 `onnx` |
| `--model` | — | onnx 模型路径 |
| `--input-name` / `--output-name` | 空（自动探测） | 模型输入输出名 |
| `--input-size` | `256` | 模型输入边长（模型固定尺寸时自动采用） |
| `--input-layout` | 空（自动探测） | `nchw` 或 `nhwc` |
| `--keypoint-layout` | `movenet` | 关键点布局 `movenet(y,x,score)` 或 `xy` |
| `--normalize-input` | 自动 | movenet 默认 0..255；显式传入则强制 0..1 |
| `--ort-lib` | `$ONNXRUNTIME_LIBRARY` | onnxruntime 动态库路径 |
| `--api-key` | 空 | 非空时启用 Bearer/query token 鉴权 |
| `--max-frame-bytes` | `3145728` | 单帧大小上限（字节） |

## HTTP 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/healthz` | 健康检查（含 provider 与协议版本） |
| POST | `/api/v1/pose/detect` | 单帧识别（multipart 字段 `frame` 或原始图像体） |
| GET | `/api/v1/ws/pose` | WebSocket 流式识别，协议见 [docs/protocol.md](docs/protocol.md) |

## 工程结构

```
cmd/server/            入口与启动参数
internal/api/          HTTP 路由、鉴权、REST 与 WS 会话（心跳/背压/错误码）
internal/pose/         关键点数据结构、Provider 接口、mock 实现
internal/pose/onnx/    ONNX Runtime 实现（-tags onnx）与占位 stub
internal/analyze/      关节角、动作计数状态机、提示 code（纯 Go 全测试）
webapp/                内嵌 Web 前端（原生 JS + Canvas，无构建链）
scripts/setup-onnx.sh  onnxruntime 环境准备
docs/protocol.md       WS 协议契约
```

## 客户端

- Android：[linuxsuren/open-fitpose-android](https://github.com/linuxsuren/open-fitpose-android)
- 鸿蒙：[linuxsuren/open-fitpose-ohos](https://github.com/linuxsuren/open-fitpose-ohos)

## License

[Apache-2.0](LICENSE)
