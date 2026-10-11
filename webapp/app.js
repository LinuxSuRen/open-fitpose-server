// FitPose Web 前端：摄像头取帧 → WebSocket 流式识别 → 骨架叠加与计数展示。
// 服务端只下发稳定 code，全部用户文案在本文件维护（展示与数据分离）。
"use strict";

// ---- 文案映射（未知 code 走兜底） ----
const TIP_TEXT = {
  SQUAT_NOT_DEEP_ENOUGH: "下蹲深度不足，再蹲低一些",
  KNEE_OVER_TOE: "膝盖超过脚尖，注意收膝",
  BACK_NOT_STRAIGHT: "躯干过度前倾，挺直腰背",
  BODY_NOT_STRAIGHT: "身体未呈一条直线，收紧核心",
  ARMS_NOT_FULLY_RAISED: "手臂未完全打开举过头顶",
};
const TIP_FALLBACK = "注意动作规范";
const TIP_ALL_GOOD = "动作标准，继续保持";

const CONN_TEXT = {
  idle: "未连接",
  connecting: "连接中…",
  open: "已连接",
  backoff: "重连中…",
  closed: "连接已断开",
};
const ERR_TEXT = {
  UNAUTHORIZED: "访问令牌不正确，请检查服务端鉴权配置",
  MODEL_NOT_READY: "服务端模型未就绪",
};

// COCO 17 点骨架连线（下标与 docs/protocol.md keypoint_names 一致）
const EDGES = [
  [5, 6], [5, 7], [7, 9], [6, 8], [8, 10],
  [5, 11], [6, 12], [11, 12],
  [11, 13], [13, 15], [12, 14], [14, 16],
];

const FRAME_MS = 100;   // 发帧间隔（约 10fps，服务端按最新帧处理）
const SEND_SIZE = 320;  // 上传缩放宽（等比）
const JPEG_QUALITY = 0.6;

const els = {
  video: document.getElementById("cam"),
  canvas: document.getElementById("overlay"),
  ctx: document.getElementById("overlay").getContext("2d"),
  stageTip: document.getElementById("stage-tip"),
  connBadge: document.getElementById("conn-badge"),
  providerBadge: document.getElementById("provider-badge"),
  tips: document.getElementById("tips"),
  wsUrl: document.getElementById("ws-url"),
  wsToken: document.getElementById("ws-token"),
  btnCam: document.getElementById("btn-cam"),
  btnConnect: document.getElementById("btn-connect"),
  counts: {
    squat: { num: document.getElementById("count-squat"), stage: document.getElementById("stage-squat") },
    pushup: { num: document.getElementById("count-pushup"), stage: document.getElementById("stage-pushup") },
    jumping_jack: { num: document.getElementById("count-jack"), stage: document.getElementById("stage-jack") },
  },
};

const state = {
  ws: null,
  seq: 0,
  connState: "idle",
  retry: 0,
  cameraOn: false,
  keypoints: null,
  frameTimer: null,
};

els.wsUrl.value = defaultWsUrl();
els.btnCam.addEventListener("click", toggleCamera);
els.btnConnect.addEventListener("click", connect);

function defaultWsUrl() {
  const { protocol, hostname } = location;
  const wsProto = protocol === "https:" ? "wss" : "ws";
  return `${wsProto}://${hostname || "127.0.0.1"}:8080/api/v1/ws/pose`;
}

// ---- 连接管理（指数退避重连） ----
function connect() {
  if (state.connState === "open" || state.connState === "connecting") {
    closeSocket();
    return;
  }
  const url = new URL(els.wsUrl.value.trim());
  const token = els.wsToken.value.trim();
  if (token) url.searchParams.set("token", token);

  setConn("connecting");
  const ws = new WebSocket(url.toString());
  state.ws = ws;

  ws.onopen = () => {
    state.retry = 0;
    setConn("open");
    ws.send(JSON.stringify({ type: "hello", protocol: 1, client: "web" }));
  };
  ws.onmessage = (ev) => handleServerMessage(ev.data);
  ws.onclose = () => {
    setConn("closed");
    if (state.retry < 8 && els.wsUrl.value.trim()) {
      const delay = Math.min(1000 * 2 ** state.retry, 8000);
      state.retry += 1;
      setConn("backoff");
      setTimeout(connect, delay);
    }
  };
  ws.onerror = () => setConn("backoff");
}

function closeSocket() {
  const ws = state.ws;
  state.ws = null;
  if (ws) {
    ws.onclose = null;
    ws.close();
  }
  setConn("idle");
}

function setConn(s) {
  state.connState = s;
  els.connBadge.textContent = CONN_TEXT[s] || s;
  els.connBadge.className = "badge " + (s === "open" ? "ok" : s === "idle" ? "" : s === "connecting" ? "warn" : "err");
  els.btnConnect.textContent = s === "open" || s === "connecting" ? "断开" : "连接服务";
}

// ---- 服务端消息处理 ----
function handleServerMessage(raw) {
  let msg;
  try { msg = JSON.parse(raw); } catch { return; }
  switch (msg.type) {
    case "welcome":
      els.providerBadge.textContent = `推理后端：${msg.provider}`;
      els.providerBadge.hidden = false;
      break;
    case "pose":
      state.keypoints = msg.keypoints;
      renderAnalysis(msg.analysis);
      renderTips(msg.tips || []);
      break;
    case "error":
      renderTips([`__error__${msg.code}`]);
      break;
    default:
      break;
  }
}

function renderAnalysis(analysis) {
  if (!analysis) return;
  for (const [action, data] of Object.entries(analysis)) {
    const ui = els.counts[action];
    if (!ui || !data) continue;
    ui.num.textContent = data.count;
    ui.stage.textContent = data.stage || "";
  }
}

function renderTips(tips) {
  els.tips.innerHTML = "";
  const items = tips
    .map((t) => (t.startsWith("__error__") ? ERR_TEXT[t.slice("__error__".length)] : null) || TIP_TEXT[t] || TIP_FALLBACK)
    .filter(Boolean);
  if (items.length === 0) {
    const li = document.createElement("li");
    li.className = "empty allgood";
    li.textContent = TIP_ALL_GOOD;
    els.tips.appendChild(li);
    return;
  }
  for (const text of items) {
    const li = document.createElement("li");
    li.textContent = text;
    els.tips.appendChild(li);
  }
}

// ---- 摄像头与画面渲染 ----
async function toggleCamera() {
  if (state.cameraOn) {
    stopCamera();
    return;
  }
  try {
    els.stageTip.hidden = true;
    els.video.srcObject = await navigator.mediaDevices.getUserMedia({
      video: { facingMode: "environment", width: { ideal: 640 }, height: { ideal: 480 } },
      audio: false,
    });
    state.cameraOn = true;
    els.btnCam.textContent = "关闭摄像头";
    await els.video.play();
    els.canvas.width = els.video.videoWidth || 640;
    els.canvas.height = els.video.videoHeight || 480;
    state.frameTimer = setInterval(sendFrame, FRAME_MS);
  } catch (err) {
    els.stageTip.hidden = false;
    els.stageTip.textContent = "无法访问摄像头，请检查浏览器权限（需 HTTPS 或 localhost）";
    state.cameraOn = false;
  }
}

function stopCamera() {
  clearInterval(state.frameTimer);
  state.frameTimer = null;
  const stream = els.video.srcObject;
  if (stream) for (const track of stream.getTracks()) track.stop();
  els.video.srcObject = null;
  state.cameraOn = false;
  state.keypoints = null;
  els.btnCam.textContent = "开启摄像头";
  els.ctx.clearRect(0, 0, els.canvas.width, els.canvas.height);
}

function sendFrame() {
  if (!state.cameraOn || !state.ws || state.ws.readyState !== WebSocket.OPEN) return;
  if (els.video.readyState < 2) return;

  const vw = els.video.videoWidth, vh = els.video.videoHeight;
  if (!vw || !vh) return;
  const scale = SEND_SIZE / vw;
  const w = SEND_SIZE, h = Math.round(vh * scale);

  const off = document.createElement("canvas");
  off.width = w; off.height = h;
  off.getContext("2d").drawImage(els.video, 0, 0, w, h);

  drawOverlay();
  off.toBlob(
    (blob) => {
      if (!blob) return;
      const reader = new FileReader();
      reader.onload = () => {
        state.seq += 1;
        state.ws.send(JSON.stringify({
          type: "frame", seq: state.seq, format: "jpeg",
          data: reader.result.split(",", 2)[1],
        }));
      };
      reader.readAsDataURL(blob);
    },
    "image/jpeg", JPEG_QUALITY,
  );
}

// 画实时画面 + 骨架；识别结果按归一化坐标等比映射
function drawOverlay() {
  const { ctx, canvas } = els;
  ctx.drawImage(els.video, 0, 0, canvas.width, canvas.height);
  const kps = state.keypoints;
  if (!kps || kps.length === 0) return;

  ctx.lineWidth = 3;
  ctx.strokeStyle = "rgba(79, 195, 247, 0.9)";
  for (const [a, b] of EDGES) {
    const ka = kps[a], kb = kps[b];
    if (!ka || !kb || ka.score < 0.3 || kb.score < 0.3) continue;
    ctx.beginPath();
    ctx.moveTo(ka.x * canvas.width, ka.y * canvas.height);
    ctx.lineTo(kb.x * canvas.width, kb.y * canvas.height);
    ctx.stroke();
  }
  ctx.fillStyle = "rgba(129, 199, 132, 0.95)";
  for (const kp of kps) {
    if (kp.score < 0.3) continue;
    ctx.beginPath();
    ctx.arc(kp.x * canvas.width, kp.y * canvas.height, 4, 0, Math.PI * 2);
    ctx.fill();
  }
}
