// fitpose-server：健身/运动姿势视觉识别服务端。
//
// 默认使用 mock 提供者即可运行（开发/演示）；生产部署用
// `-tags onnx` 构建并加载真实姿态模型，见 README。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/linuxsuren/open-fitpose-server/internal/api"
	"github.com/linuxsuren/open-fitpose-server/internal/pose"
	"github.com/linuxsuren/open-fitpose-server/internal/pose/onnx"
	"github.com/linuxsuren/open-fitpose-server/webapp"
)

func main() {
	var (
		addr           = flag.String("addr", ":8080", "HTTP 监听地址")
		providerName   = flag.String("provider", "mock", "推理提供者: mock | onnx")
		modelPath      = flag.String("model", "", "onnx: 姿态模型路径")
		inputName      = flag.String("input-name", "", "onnx: 模型输入名（空=自动取模型第一个输入）")
		outputName     = flag.String("output-name", "", "onnx: 模型输出名（空=自动取模型第一个输出）")
		inputSize      = flag.Int("input-size", 256, "onnx: 模型输入边长（模型固定尺寸时自动采用）")
		inputLayout    = flag.String("input-layout", "", "onnx: 输入布局 nchw|nhwc（空=按模型维度自动探测）")
		keypointLayout = flag.String("keypoint-layout", "movenet", "onnx: 关键点三元组布局 movenet(y,x,score)|xy(x,y,score)")
		normalize      = flag.Bool("normalize-input", false, "onnx: 输入像素归一化 0..1（movenet 布局默认 0..255，此开关强制 0..1）")
		ortLib         = flag.String("ort-lib", os.Getenv("ONNXRUNTIME_LIBRARY"), "onnx: onnxruntime 动态库路径（默认取 ONNXRUNTIME_LIBRARY 环境变量）")
		apiKey         = flag.String("api-key", "", "非空时启用 Bearer/query token 鉴权")
		maxFrameBytes  = flag.Int64("max-frame-bytes", 3<<20, "单帧图像大小上限")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var provider pose.Provider
	switch *providerName {
	case "mock":
		provider = pose.NewMockProvider()
	case "onnx":
		opts := onnx.Options{
			ModelPath:   *modelPath,
			InputName:   *inputName,
			OutputName:  *outputName,
			InputSize:   *inputSize,
			Layout:      *keypointLayout,
			InputLayout: *inputLayout,
			ORTLibPath:  *ortLib,
		}
		// 仅当用户显式传入 --normalize-input 时才覆盖自动推断
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "normalize-input" {
				opts.Normalize = normalize
			}
		})
		p, err := onnx.New(opts)
		if err != nil {
			logger.Error("init onnx provider failed", "err", err)
			os.Exit(1)
		}
		provider = p
	default:
		logger.Error("unknown provider", "provider", *providerName)
		os.Exit(1)
	}

	srv := api.NewServer(api.Options{
		Provider:      provider,
		WebFS:         webapp.FS,
		APIKey:        *apiKey,
		MaxFrameBytes: *maxFrameBytes,
		Logger:        logger,
	})
	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler()}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	logger.Info("fitpose server listening",
		"addr", *addr, "provider", provider.Name(), "auth", *apiKey != "")
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}
