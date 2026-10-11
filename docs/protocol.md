# FitPose 流式识别协议（v1）

WebSocket 端点：`GET /api/v1/ws/pose`（升级请求）。

- 传输：WebSocket，消息为 UTF-8 JSON 文本帧，字段统一 snake_case。
- 鉴权：服务端启用 `--api-key` 时，连接需携带
  `Authorization: Bearer <key>` 头或 `?token=<key>` 查询参数；未配置则不鉴权。
- 心跳：客户端可随时发送应用层 `ping`；同时服务端每 30s 发送协议层
  ping，客户端需响应 pong（大多数客户端库自动处理），75s 无读活动服务端将断开连接。
- 背压：服务端每次只处理最新一帧，处理期间收到的旧帧直接丢弃。
  客户端应以固定节拍发送（建议 5-10 fps），并以 `seq` 关联请求与响应。
- 断线重连：客户端自行重连，建议指数退避（1s 起步、上限 8s），
  重连成功后重新发送 `hello`。

## 客户端上行消息

### hello

连接后首条消息，声明客户端身份与协议版本。

```json
{"type": "hello", "protocol": 1, "client": "web|android|ohos"}
```

### frame

一帧待识别图像。

```json
{"type": "frame", "seq": 42, "format": "jpeg", "data": "<base64>"}
```

- `seq`：客户端自增序号，服务端在对应响应中原样回传。
- `format`：图像编码，当前仅支持 `jpeg`。
- `data`：图像的 base64（标准编码，不带 data: 前缀）。

### ping

```json
{"type": "ping", "ts": 1699999999000}
```

## 服务端下行消息

### welcome

对 `hello` 的应答。

```json
{
  "type": "welcome",
  "protocol": 1,
  "provider": "mock",
  "keypoint_names": ["nose", "left_eye", "right_eye", "..."]
}
```

`keypoint_names` 为 COCO 17 点名称，数组下标即后续 keypoints 下标，跨版本稳定。

### pose

对 `frame` 的应答。

```json
{
  "type": "pose",
  "seq": 42,
  "server_ms": 12,
  "keypoints": [
    {"name": "nose", "x": 0.51, "y": 0.32, "score": 0.93}
  ],
  "analysis": {
    "squat":        {"count": 3, "stage": "down"},
    "pushup":       {"count": 0, "stage": "idle"},
    "jumping_jack": {"count": 0, "stage": "idle"}
  },
  "tips": ["KNEE_OVER_TOE"]
}
```

- `keypoints`：固定 17 项，`x`/`y` 为相对原图宽高的归一化坐标（[0,1]），
  `score` 为置信度（低于 0.3 的点不应参与展示与判定）。
- `analysis.stage`：`idle` / `up` / `down`（深蹲、俯卧撑）或
  `idle` / `open` / `closed`（开合跳）。
- `server_ms`：服务端处理本帧耗时。

### pong

对 `ping` 的应答，`ts` 原样回传。

### error

```json
{"type": "error", "seq": 43, "code": "BAD_FRAME", "detail": "base64 decode failed"}
```

`detail` 仅供排查日志，**客户端展示必须以 `code` 映射本地文案**，未知 code 走兜底文案。

| code | 含义 | 客户端建议处理 |
|---|---|---|
| `UNAUTHORIZED` | 令牌缺失或不正确 | 提示检查服务地址与令牌配置 |
| `BAD_MESSAGE` | JSON 非法或 type 未知 | 记录日志，忽略该消息 |
| `UNSUPPORTED_FORMAT` | 图像编码不支持 | 提示仅支持 JPEG |
| `BAD_FRAME` | base64 解码失败或空帧 | 检查采集与编码逻辑 |
| `FRAME_TOO_LARGE` | 帧超过大小上限 | 降低分辨率或压缩质量 |
| `INFERENCE_FAILED` | 推理后端失败 | 提示服务暂不可用，自动重试 |

## 姿态提示 code

| code | 含义（文案由客户端本地定义） |
|---|---|
| `SQUAT_NOT_DEEP_ENOUGH` | 深蹲深度不足 |
| `KNEE_OVER_TOE` | 膝盖超过脚尖 |
| `BACK_NOT_STRAIGHT` | 深蹲躯干过度前倾 |
| `BODY_NOT_STRAIGHT` | 俯卧撑身体未呈直线 |
| `ARMS_NOT_FULLY_RAISED` | 开合跳手臂未完全打开 |
