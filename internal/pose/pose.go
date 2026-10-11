// Package pose 定义姿态识别的公共数据结构与推理提供者接口。
//
// 关键点遵循 COCO 17 点规范，坐标统一归一化到 [0,1]，
// 相对原图宽高，便于各端（Web/Android/鸿蒙）按预览画布等比映射。
package pose

import "context"

// KeypointNames COCO 17 关键点名称，下标即网络输出顺序，跨端保持稳定。
var KeypointNames = []string{
	"nose",
	"left_eye", "right_eye",
	"left_ear", "right_ear",
	"left_shoulder", "right_shoulder",
	"left_elbow", "right_elbow",
	"left_wrist", "right_wrist",
	"left_hip", "right_hip",
	"left_knee", "right_knee",
	"left_ankle", "right_ankle",
}

// COCO 关键点下标常量，供分析器按语义取点。
const (
	Nose = iota
	LeftEye
	RightEye
	LeftEar
	RightEar
	LeftShoulder
	RightShoulder
	LeftElbow
	RightElbow
	LeftWrist
	RightWrist
	LeftHip
	RightHip
	LeftKnee
	RightKnee
	LeftAnkle
	RightAnkle
)

// Keypoint 单个关键点，x/y 为相对原图宽高的归一化坐标。
type Keypoint struct {
	Name  string  `json:"name"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Score float64 `json:"score"`
}

// Pose 一帧人体姿态，长度恒为 len(KeypointNames)。
type Pose struct {
	Keypoints []Keypoint `json:"keypoints"`
}

// Provider 姿态推理提供者。
//
// frame 为原始图像字节（JPEG），实现方负责解码与预处理；
// 返回的 Pose 坐标必须归一化到 [0,1]。
type Provider interface {
	// Name 返回提供者标识（如 mock、onnx），用于健康检查与欢迎消息。
	Name() string
	// Detect 对单帧图像执行姿态识别。
	Detect(ctx context.Context, frame []byte) (Pose, error)
}

// Empty 判断姿态是否为空（未检出任何点）。
func (p Pose) Empty() bool {
	return len(p.Keypoints) == 0
}

// At 按下标安全取点，越界返回零值。
func (p Pose) At(i int) Keypoint {
	if i < 0 || i >= len(p.Keypoints) {
		return Keypoint{}
	}
	return p.Keypoints[i]
}
