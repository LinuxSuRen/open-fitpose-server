package pose

import (
	"context"
	"hash/fnv"
	"math"
)

// MockProvider 确定性模拟提供者，用于本地开发、演示与自动化测试。
//
// 不解码图像，而是由帧内容的哈希决定一个"下蹲-站起"循环中的相位，
// 生成一套合理的关键点，保证：
//   - 相同输入恒定输出（可测试）
//   - 相机对着任何画面都能看到骨架动起来（可演示）
type MockProvider struct {
	// DetectFn 可注入自定义生成逻辑，测试用；为空时走默认合成。
	DetectFn func(frame []byte) Pose
}

// NewMockProvider 创建默认的模拟提供者。
func NewMockProvider() *MockProvider {
	return &MockProvider{}
}

// Name 实现 Provider。
func (MockProvider) Name() string { return "mock" }

// Detect 实现 Provider，输出由帧内容哈希决定。
func (m *MockProvider) Detect(_ context.Context, frame []byte) (Pose, error) {
	if m.DetectFn != nil {
		return m.DetectFn(frame), nil
	}
	return synthPose(mockPhase(frame)), nil
}

// mockPhase 将帧哈希映射到 [0,1) 相位。
func mockPhase(frame []byte) float64 {
	h := fnv.New64a()
	_, _ = h.Write(frame)
	return float64(h.Sum64()%1000) / 1000
}

// synthPose 按相位合成一个站立与深蹲之间过渡的人体。
func synthPose(phase float64) Pose {
	// 相位在 [0,1) 内往复映射为 [0,1] 的下蹲深度
	t := 0.5 - 0.5*math.Cos(2*math.Pi*phase)

	hipY := 0.50 + 0.12*t   // 下蹲时髋部下移
	kneeY := 0.72 + 0.04*t  // 膝盖略下移
	kneeDX := 0.04 + 0.05*t // 屈膝时膝盖外移，表现屈曲

	kp := make([]Keypoint, len(KeypointNames))
	set := func(idx int, x, y float64) {
		kp[idx] = Keypoint{Name: KeypointNames[idx], X: x, Y: y, Score: 0.9}
	}
	set(Nose, 0.5, 0.12)
	set(LeftEye, 0.48, 0.11)
	set(RightEye, 0.52, 0.11)
	set(LeftEar, 0.46, 0.13)
	set(RightEar, 0.54, 0.13)
	set(LeftShoulder, 0.40, 0.25)
	set(RightShoulder, 0.60, 0.25)
	set(LeftElbow, 0.36, 0.38)
	set(RightElbow, 0.64, 0.38)
	set(LeftWrist, 0.34, 0.50)
	set(RightWrist, 0.66, 0.50)
	set(LeftHip, 0.44, hipY)
	set(RightHip, 0.56, hipY)
	set(LeftKnee, 0.44-kneeDX, kneeY)
	set(RightKnee, 0.56+kneeDX, kneeY)
	set(LeftAnkle, 0.45, 0.92)
	set(RightAnkle, 0.55, 0.92)

	return Pose{Keypoints: kp}
}
