//go:build !onnx

// Package onnx 未启用 `-tags onnx` 构建时的占位实现，
// 选择 onnx 提供者会得到明确的构建提示。
package onnx

import (
	"context"
	"errors"

	"github.com/linuxsuren/open-fitpose-server/internal/pose"
)

// Options 与 onnx 构建下保持同构，便于 main 无差别调用。
type Options struct {
	ModelPath  string
	InputName  string
	OutputName string
	InputSize  int
	Layout     string
}

// Provider 占位提供者，永远返回构建提示错误。
type Provider struct{}

// New 返回错误：需要带 onnx 标签重新构建。
func New(Options) (*Provider, error) {
	return nil, errors.New("onnx provider is not compiled in; rebuild with -tags onnx (see README)")
}

// Name 实现 pose.Provider。
func (*Provider) Name() string { return "onnx" }

// Detect 实现 pose.Provider。
func (*Provider) Detect(_ context.Context, _ []byte) (pose.Pose, error) {
	return pose.Pose{}, errors.New("onnx provider is not compiled in")
}
