package analyze

import (
	"math"
	"testing"

	"github.com/linuxsuren/open-fitpose-server/internal/pose"
)

// mkPose 构造全部高置信度关键点，坐标由覆盖函数决定。
func mkPose(over map[int]pose.Keypoint) pose.Pose {
	kp := make([]pose.Keypoint, len(pose.KeypointNames))
	for i, name := range pose.KeypointNames {
		kp[i] = pose.Keypoint{Name: name, X: 0.5, Y: 0.5, Score: 0.9}
	}
	for i, k := range over {
		k.Name = pose.KeypointNames[i]
		kp[i] = k
	}
	return pose.Pose{Keypoints: kp}
}

// squatPose 生成指定膝角（度）的侧面深蹲姿态，髋-躯干保持竖直。
func squatPose(kneeDeg, hipDeg float64) pose.Pose {
	hip := [2]float64{0.5, 0.40}
	knee := [2]float64{0.5, 0.60}
	ankle := rotateTo(hip, knee, kneeDeg, 0.20)
	shoulder := rotateTo(knee, hip, hipDeg, 0.25) // 髋以上的躯干方向
	return mkPose(map[int]pose.Keypoint{
		pose.LeftHip:       {X: hip[0], Y: hip[1], Score: 0.9},
		pose.RightHip:      {X: hip[0], Y: hip[1], Score: 0.9},
		pose.LeftKnee:      {X: knee[0], Y: knee[1], Score: 0.9},
		pose.RightKnee:     {X: knee[0], Y: knee[1], Score: 0.9},
		pose.LeftAnkle:     {X: ankle[0], Y: ankle[1], Score: 0.9},
		pose.RightAnkle:    {X: ankle[0], Y: ankle[1], Score: 0.9},
		pose.LeftShoulder:  {X: shoulder[0], Y: shoulder[1], Score: 0.9},
		pose.RightShoulder: {X: shoulder[0], Y: shoulder[1], Score: 0.9},
	})
}

// pushUpPose 生成指定肘角与身体直线角的俯卧撑姿态。
func pushUpPose(elbowDeg, bodyDeg float64) pose.Pose {
	rad := math.Pi / 180
	elbow := [2]float64{0.5, 0.60}
	// 肘部两条臂向量以竖直方向对称张开，夹角恰为 elbowDeg
	shoulder := [2]float64{
		elbow[0] + 0.2*math.Cos((90+elbowDeg/2)*rad),
		elbow[1] + 0.2*math.Sin((90+elbowDeg/2)*rad),
	}
	wrist := [2]float64{
		elbow[0] + 0.2*math.Cos((90-elbowDeg/2)*rad),
		elbow[1] + 0.2*math.Sin((90-elbowDeg/2)*rad),
	}
	// 髋在肘-躯干延长线上，膝相对髋张成 bodyDeg
	hip := rotateTo(shoulder, elbow, bodyDeg, 0.25)
	knee := rotateTo(shoulder, hip, bodyDeg, 0.25)
	return mkPose(map[int]pose.Keypoint{
		pose.LeftElbow:     {X: elbow[0], Y: elbow[1], Score: 0.9},
		pose.RightElbow:    {X: elbow[0], Y: elbow[1], Score: 0.9},
		pose.LeftShoulder:  {X: shoulder[0], Y: shoulder[1], Score: 0.9},
		pose.RightShoulder: {X: shoulder[0], Y: shoulder[1], Score: 0.9},
		pose.LeftWrist:     {X: wrist[0], Y: wrist[1], Score: 0.9},
		pose.RightWrist:    {X: wrist[0], Y: wrist[1], Score: 0.9},
		pose.LeftHip:       {X: hip[0], Y: hip[1], Score: 0.9},
		pose.RightHip:      {X: hip[0], Y: hip[1], Score: 0.9},
		pose.LeftKnee:      {X: knee[0], Y: knee[1], Score: 0.9},
		pose.RightKnee:     {X: knee[0], Y: knee[1], Score: 0.9},
	})
}

// rotateTo 返回从 base 出发、与 ref-base 夹角为 deg、长度 r 的端点。
func rotateTo(ref, base [2]float64, deg, r float64) [2]float64 {
	ang := math.Atan2(ref[1]-base[1], ref[0]-base[0]) + deg*math.Pi/180
	return [2]float64{base[0] + r*math.Cos(ang), base[1] + r*math.Sin(ang)}
}

func TestSquatCount(t *testing.T) {
	an := NewAnalyzer()
	seq := []float64{170, 85, 170, 85, 170} // 站→蹲→站→蹲→站
	for _, knee := range seq {
		_, _ = an.Update(squatPose(knee, 170))
	}
	got := an.squat.count
	if got != 2 {
		t.Fatalf("squat count = %d, want 2", got)
	}
}

func TestSquatTips(t *testing.T) {
	an := NewAnalyzer()
	_, tips := an.Update(squatPose(120, 170)) // 蹲了但不够深
	if !contains(tips, TipSquatNotDeepEnough) {
		t.Fatalf("want tip %s, got %v", TipSquatNotDeepEnough, tips)
	}
	_, tips = an.Update(squatPose(85, 120)) // 躯干前倾
	if !contains(tips, TipBackNotStraight) {
		t.Fatalf("want tip %s, got %v", TipBackNotStraight, tips)
	}
}

func TestSquatLowConfidenceIdle(t *testing.T) {
	an := NewAnalyzer()
	p := squatPose(85, 170)
	for i := range p.Keypoints {
		p.Keypoints[i].Score = 0.1 // 全部低置信度
	}
	analysis, _ := an.Update(p)
	if analysis.Squat.Stage != StageIdle {
		t.Fatalf("low confidence should stay idle, got %s", analysis.Squat.Stage)
	}
}

func TestPushUpCountAndTip(t *testing.T) {
	an := NewAnalyzer()
	for _, elbow := range []float64{170, 90, 170, 90, 170} {
		_, _ = an.Update(pushUpPose(elbow, 180))
	}
	if an.pushUp.count != 2 {
		t.Fatalf("pushup count = %d, want 2", an.pushUp.count)
	}

	an2 := NewAnalyzer()
	_, tips := an2.Update(pushUpPose(90, 120)) // 身体塌陷
	if !contains(tips, TipBodyNotStraight) {
		t.Fatalf("want tip %s, got %v", TipBodyNotStraight, tips)
	}
}

func TestJumpingJack(t *testing.T) {
	open := jackPose(true)
	closed := jackPose(false)

	an := NewAnalyzer()
	for i := 0; i < 3; i++ {
		analysis, tips := an.Update(open)
		_ = tips
		if analysis.JumpingJack.Stage != StageOpen {
			t.Fatalf("iter %d: stage = %s, want open", i, analysis.JumpingJack.Stage)
		}
		analysis, _ = an.Update(closed)
		if analysis.JumpingJack.Stage != StageClosed {
			t.Fatalf("iter %d: stage = %s, want closed", i, analysis.JumpingJack.Stage)
		}
	}
	if an.jumpingJack.count != 3 {
		t.Fatalf("jumping jack count = %d, want 3", an.jumpingJack.count)
	}
}

func TestJumpingJackArmTip(t *testing.T) {
	an := NewAnalyzer()
	_, _ = an.Update(jackPose(true))
	// 分腿仍开，但手臂放下：应提示手臂未完全举起
	p := jackPose(false)
	kp := p.Keypoints
	kp[pose.LeftAnkle].X = 0.2
	kp[pose.RightAnkle].X = 0.8
	_, tips := an.Update(p)
	if !contains(tips, TipArmsNotFullyRaised) {
		t.Fatalf("want tip %s, got %v", TipArmsNotFullyRaised, tips)
	}
}

// jackPose 生成开合跳 open/closed 关键姿态。
func jackPose(open bool) pose.Pose {
	p := mkPose(nil)
	kp := p.Keypoints
	kp[pose.LeftShoulder].X, kp[pose.LeftShoulder].Y = 0.40, 0.30
	kp[pose.RightShoulder].X, kp[pose.RightShoulder].Y = 0.60, 0.30
	kp[pose.Nose].X, kp[pose.Nose].Y = 0.5, 0.20
	if open {
		kp[pose.LeftWrist].X, kp[pose.LeftWrist].Y = 0.35, 0.10
		kp[pose.RightWrist].X, kp[pose.RightWrist].Y = 0.65, 0.10
		kp[pose.LeftAnkle].X, kp[pose.LeftAnkle].Y = 0.25, 0.90
		kp[pose.RightAnkle].X, kp[pose.RightAnkle].Y = 0.75, 0.90
	} else {
		kp[pose.LeftWrist].X, kp[pose.LeftWrist].Y = 0.38, 0.50
		kp[pose.RightWrist].X, kp[pose.RightWrist].Y = 0.62, 0.50
		kp[pose.LeftAnkle].X, kp[pose.LeftAnkle].Y = 0.45, 0.90
		kp[pose.RightAnkle].X, kp[pose.RightAnkle].Y = 0.55, 0.90
	}
	return p
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
