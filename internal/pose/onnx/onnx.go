//go:build onnx

// Package onnx 基于 ONNX Runtime 的真实姿态推理实现。
//
// 仅在 `-tags onnx` 构建时编译，需要本机存在 onnxruntime 动态库，
// 详见 README「ONNX 部署」与 scripts/setup-onnx.sh。
package onnx

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/linuxsuren/open-fitpose-server/internal/pose"
)

// OutputLayout 关键点三元组的内存布局。
type OutputLayout string

const (
	// LayoutMoveNet MoveNet 系列：(y, x, score)，坐标为输入分辨率像素值。
	LayoutMoveNet OutputLayout = "movenet"
	// LayoutXY 通用 (x, y, score) 布局。
	LayoutXY OutputLayout = "xy"
)

// Options ONNX 提供者配置，与模型实际输入输出名严格对应。
type Options struct {
	ModelPath  string
	InputName  string // 默认 input
	OutputName string // 默认 output
	InputSize  int    // 正方形输入边长，默认 256
	Layout     OutputLayout
}

// Provider ONNX Runtime 推理提供者，会话级串行执行。
type Provider struct {
	mu      sync.Mutex
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	output  *ort.Tensor[float32]
	size    int
	layout  OutputLayout
}

// New 初始化 ONNX 会话，模型不存在或名称不匹配将启动失败。
func New(opts Options) (*Provider, error) {
	if opts.ModelPath == "" {
		return nil, fmt.Errorf("onnx provider requires --model")
	}
	if opts.InputSize <= 0 {
		opts.InputSize = 256
	}
	if opts.InputName == "" {
		opts.InputName = "input"
	}
	if opts.OutputName == "" {
		opts.OutputName = "output"
	}
	if opts.Layout == "" {
		opts.Layout = LayoutMoveNet
	}

	n := opts.InputSize
	input, err := ort.NewTensor(
		ort.NewShape(1, 3, int64(n), int64(n)), make([]float32, 3*n*n))
	if err != nil {
		return nil, fmt.Errorf("create input tensor: %w", err)
	}
	output, err := ort.NewTensor(ort.NewShape(1, 17, 3), make([]float32, 17*3))
	if err != nil {
		return nil, fmt.Errorf("create output tensor: %w", err)
	}
	session, err := ort.NewAdvancedSession(opts.ModelPath,
		[]string{opts.InputName}, []string{opts.OutputName},
		[]ort.AnyTensor{input}, []ort.AnyTensor{output}, nil)
	if err != nil {
		return nil, fmt.Errorf("create onnx session (check --model and input/output names): %w", err)
	}
	return &Provider{
		session: session, input: input, output: output,
		size: n, layout: opts.Layout,
	}, nil
}

// Name 实现 pose.Provider。
func (*Provider) Name() string { return "onnx" }

// Detect 实现 pose.Provider：解码图像 → 缩放填充 NCHW 张量 → 推理 → 归一化关键点。
func (p *Provider) Detect(_ context.Context, frame []byte) (pose.Pose, error) {
	img, _, err := image.Decode(bytes.NewReader(frame))
	if err != nil {
		return pose.Pose{}, fmt.Errorf("decode image: %w", err)
	}
	fillInput(p.input.GetData(), img, p.size)

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.session.Run(); err != nil {
		return pose.Pose{}, fmt.Errorf("run inference: %w", err)
	}
	return p.parseOutput(), nil
}

// fillInput 最近邻缩放到 n×n，RGB 归一化 0..1，NCHW 布局。
func fillInput(data []float32, img image.Image, n int) {
	b := img.Bounds()
	plane := n * n
	for y := 0; y < n; y++ {
		sy := b.Min.Y + y*b.Dy()/n
		for x := 0; x < n; x++ {
			sx := b.Min.X + x*b.Dx()/n
			r, g, bl, _ := img.At(sx, sy).RGBA()
			idx := y*n + x
			data[idx] = float32(r>>8) / 255
			data[plane+idx] = float32(g>>8) / 255
			data[2*plane+idx] = float32(bl>>8) / 255
		}
	}
}

// parseOutput 读取输出张量并按布局归一化到 [0,1]。
func (p *Provider) parseOutput() pose.Pose {
	out := p.output.GetData()
	kp := make([]pose.Keypoint, len(pose.KeypointNames))
	for i := 0; i < len(kp) && (i*3+2) < len(out); i++ {
		var x, y, score float64
		switch p.layout {
		case LayoutXY:
			x, y, score = float64(out[i*3]), float64(out[i*3+1]), float64(out[i*3+2])
		default: // MoveNet: (y, x, score)
			y, x, score = float64(out[i*3]), float64(out[i*3+1]), float64(out[i*3+2])
		}
		kp[i] = pose.Keypoint{
			Name:  pose.KeypointNames[i],
			X:     x / float64(p.size),
			Y:     y / float64(p.size),
			Score: score,
		}
	}
	return pose.Pose{Keypoints: kp}
}
