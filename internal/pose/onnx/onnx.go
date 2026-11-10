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

// 输入张量布局。
const (
	InputLayoutNCHW = "nchw"
	InputLayoutNHWC = "nhwc"
)

// Options ONNX 提供者配置。
//
// 未显式指定的项（输入输出名、尺寸、布局）会从模型元数据自动探测：
// 取模型的第一个输入/输出，按维度区分 NCHW/NHWC 并提取固定边长。
type Options struct {
	ModelPath   string
	InputName   string // 默认取模型第一个输入
	OutputName  string // 默认取模型第一个输出
	InputSize   int    // 模型为动态尺寸时的兜底边长，默认 256
	Layout      string // 关键点三元组布局 movenet|xy，默认 movenet
	InputLayout string // nchw | nhwc，默认按模型维度自动探测
	Normalize   *bool  // 像素是否归一化 0..1；nil 表示按布局推断（movenet: 0..255）
	ORTLibPath  string // onnxruntime 动态库路径，空则沿用进程已有加载路径
}

// Provider ONNX Runtime 推理提供者，会话级串行执行。
type Provider struct {
	mu        sync.Mutex
	session   *ort.AdvancedSession
	input     *ort.Tensor[float32]
	output    *ort.Tensor[float32]
	size      int
	nhwc      bool
	normalize bool
	layout    OutputLayout
}

// New 初始化 ONNX 会话，模型不存在或形状不兼容将启动失败。
func New(opts Options) (*Provider, error) {
	if opts.ModelPath == "" {
		return nil, fmt.Errorf("onnx provider requires --model")
	}
	if !ort.IsInitialized() {
		// 运行时按需 dlopen onnxruntime 动态库；未指定时依赖进程链接路径
		if opts.ORTLibPath != "" {
			ort.SetSharedLibraryPath(opts.ORTLibPath)
		}
		if err := ort.InitializeEnvironment(); err != nil {
			return nil, fmt.Errorf("init onnxruntime: %w", err)
		}
	}
	if opts.Layout == "" {
		opts.Layout = string(LayoutMoveNet)
	}

	inInfos, outInfos, err := ort.GetInputOutputInfo(opts.ModelPath)
	if err != nil {
		return nil, fmt.Errorf("read model info: %w", err)
	}
	if len(inInfos) == 0 || len(outInfos) == 0 {
		return nil, fmt.Errorf("model has no input or output")
	}

	inName, outName := opts.InputName, opts.OutputName
	if inName == "" {
		inName = inInfos[0].Name
	}
	if outName == "" {
		outName = outInfos[0].Name
	}

	size, nhwc, err := resolveInputShape(inInfos, inName, opts.InputSize, opts.InputLayout)
	if err != nil {
		return nil, err
	}

	normalize := true // 默认 0..1；MoveNet 官方输入为 0..255 像素值
	if opts.Normalize != nil {
		normalize = *opts.Normalize
	} else if opts.Layout == string(LayoutMoveNet) {
		normalize = false
	}

	input, err := ort.NewTensor(inputShape(size, nhwc), make([]float32, 3*size*size))
	if err != nil {
		return nil, fmt.Errorf("create input tensor: %w", err)
	}
	outShape, err := resolveOutputShape(outInfos, outName)
	if err != nil {
		return nil, err
	}
	output, err := ort.NewTensor(outShape, make([]float32, 17*3))
	if err != nil {
		return nil, fmt.Errorf("create output tensor: %w", err)
	}
	session, err := ort.NewAdvancedSession(opts.ModelPath,
		[]string{inName}, []string{outName},
		[]ort.Value{input}, []ort.Value{output}, nil)
	if err != nil {
		return nil, fmt.Errorf("create onnx session (check --model and input/output names): %w", err)
	}
	return &Provider{
		session: session, input: input, output: output,
		size: size, nhwc: nhwc, normalize: normalize, layout: OutputLayout(opts.Layout),
	}, nil
}

// resolveInputShape 从模型输入维度推断边长与 NCHW/NHWC 布局。
// 维度未知（动态维度 0 或 -1）时回退到 fallbackSize 与显式指定的 layout。
func resolveInputShape(infos []ort.InputOutputInfo, name string, fallbackSize int, explicit string) (size int, nhwc bool, err error) {
	var dims ort.Shape
	for _, info := range infos {
		if info.Name == name {
			dims = info.Dimensions
			break
		}
	}
	if fallbackSize <= 0 {
		fallbackSize = 256
	}
	switch explicit {
	case InputLayoutNCHW, InputLayoutNHWC:
		nhwc = explicit == InputLayoutNHWC
	case "":
	default:
		return 0, false, fmt.Errorf("invalid input layout %q (nchw|nhwc)", explicit)
	}
	size = fallbackSize
	if len(dims) != 4 {
		return size, nhwc, nil // 动态或非常规形状，信任调用方配置
	}
	is := func(d int64, want int64) bool { return d == want }
	sq := func(a, b int64) bool { return a > 0 && a == b }
	switch {
	case is(dims[1], 3) && sq(dims[2], dims[3]): // [1,3,N,N]
		if explicit == "" {
			nhwc = false
		}
		if n := int(dims[2]); n > 0 {
			size = n
		}
	case is(dims[3], 3) && sq(dims[1], dims[2]): // [1,N,N,3]
		if explicit == "" {
			nhwc = true
		}
		if n := int(dims[1]); n > 0 {
			size = n
		}
	}
	return size, nhwc, nil
}

// resolveOutputShape 校验输出元素数并返回张量形状（兼容 [1,17,3] 与 [1,1,17,3]）。
func resolveOutputShape(infos []ort.InputOutputInfo, name string) (ort.Shape, error) {
	for _, info := range infos {
		if info.Name != name {
			continue
		}
		n := int64(1)
		for _, d := range info.Dimensions {
			if d <= 0 {
				return nil, fmt.Errorf("model output %q has dynamic shape", name)
			}
			n *= d
		}
		if n != 17*3 {
			return nil, fmt.Errorf("model output %q has %d elements, want 17*3", name, n)
		}
		return info.Dimensions, nil
	}
	return nil, fmt.Errorf("model output %q not found", name)
}

func inputShape(size int, nhwc bool) ort.Shape {
	if nhwc {
		return ort.NewShape(1, int64(size), int64(size), 3)
	}
	return ort.NewShape(1, 3, int64(size), int64(size))
}

// Name 实现 pose.Provider。
func (*Provider) Name() string { return "onnx" }

// Detect 实现 pose.Provider：解码图像 → 缩放填充张量 → 推理 → 归一化关键点。
func (p *Provider) Detect(_ context.Context, frame []byte) (pose.Pose, error) {
	img, _, err := image.Decode(bytes.NewReader(frame))
	if err != nil {
		return pose.Pose{}, fmt.Errorf("decode image: %w", err)
	}
	if p.nhwc {
		fillInputNHWC(p.input.GetData(), img, p.size, p.normalize)
	} else {
		fillInputNCHW(p.input.GetData(), img, p.size, p.normalize)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.session.Run(); err != nil {
		return pose.Pose{}, fmt.Errorf("run inference: %w", err)
	}
	return p.parseOutput(), nil
}

// fillInputNCHW 最近邻缩放到 n×n，NCHW 布局；normalize 为 true 时归一化 0..1。
func fillInputNCHW(data []float32, img image.Image, n int, normalize bool) {
	b := img.Bounds()
	plane := n * n
	for y := 0; y < n; y++ {
		sy := b.Min.Y + y*b.Dy()/n
		for x := 0; x < n; x++ {
			sx := b.Min.X + x*b.Dx()/n
			r, g, bl, _ := img.At(sx, sy).RGBA()
			idx := y*n + x
			data[idx] = px(r>>8, normalize)
			data[plane+idx] = px(g>>8, normalize)
			data[2*plane+idx] = px(bl>>8, normalize)
		}
	}
}

// fillInputNHWC 最近邻缩放到 n×n，NHWC 布局。
func fillInputNHWC(data []float32, img image.Image, n int, normalize bool) {
	b := img.Bounds()
	for y := 0; y < n; y++ {
		sy := b.Min.Y + y*b.Dy()/n
		for x := 0; x < n; x++ {
			sx := b.Min.X + x*b.Dx()/n
			r, g, bl, _ := img.At(sx, sy).RGBA()
			idx := (y*n + x) * 3
			data[idx] = px(r>>8, normalize)
			data[idx+1] = px(g>>8, normalize)
			data[idx+2] = px(bl>>8, normalize)
		}
	}
}

func px(v uint32, normalize bool) float32 {
	if normalize {
		return float32(v) / 255
	}
	return float32(v)
}

// parseOutput 读取输出张量并解析关键点。
// 输出按 17 组三元组连续存放（[1,17,3] 与 [1,1,17,3] 内存布局一致）。
// MoveNet 系列输出的 (y, x) 已归一化到 [0,1]；xy 布局按输入分辨率像素值解析。
func (p *Provider) parseOutput() pose.Pose {
	out := p.output.GetData()
	kp := make([]pose.Keypoint, len(pose.KeypointNames))
	for i := 0; i < len(kp) && (i*3+2) < len(out); i++ {
		var x, y, score float64
		switch p.layout {
		case LayoutXY:
			x, y, score = float64(out[i*3]), float64(out[i*3+1]), float64(out[i*3+2])
			x /= float64(p.size)
			y /= float64(p.size)
		default: // MoveNet: (y, x, score)，坐标已归一化
			y, x, score = float64(out[i*3]), float64(out[i*3+1]), float64(out[i*3+2])
		}
		kp[i] = pose.Keypoint{
			Name:  pose.KeypointNames[i],
			X:     x,
			Y:     y,
			Score: score,
		}
	}
	return pose.Pose{Keypoints: kp}
}
