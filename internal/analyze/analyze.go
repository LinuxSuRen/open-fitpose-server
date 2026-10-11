// Package analyze 基于关键点的动作分析：关节角计算、动作计数与姿态纠错提示。
//
// 全部为纯 Go 逻辑，可完整单元测试；提示以稳定 code 返回，
// 文案由各端自行映射（展示与数据分离）。
package analyze

import (
	"math"

	"github.com/linuxsuren/open-fitpose-server/internal/pose"
)

// 姿态纠错提示 code（各端本地映射文案，未知 code 走兜底文案）。
const (
	TipSquatNotDeepEnough = "SQUAT_NOT_DEEP_ENOUGH" // 下蹲深度不足
	TipKneeOverToe        = "KNEE_OVER_TOE"         // 膝盖超过脚尖
	TipBackNotStraight    = "BACK_NOT_STRAIGHT"     // 深蹲躯干过度前倾
	TipBodyNotStraight    = "BODY_NOT_STRAIGHT"     // 俯卧撑身体未呈直线
	TipArmsNotFullyRaised = "ARMS_NOT_FULLY_RAISED" // 开合跳手臂未完全打开
)

// 动作阶段。
const (
	StageIdle   = "idle"
	StageUp     = "up"
	StageDown   = "down"
	StageClosed = "closed"
	StageOpen   = "open"
)

// scoreGate 参与判定/计数的关键点最低置信度。
const scoreGate = 0.3

// ActionStatus 单个动作的计数与阶段。
type ActionStatus struct {
	Count int    `json:"count"`
	Stage string `json:"stage"`
}

// Analysis 一帧的分析结果，字段命名 snake_case，跨端契约见 docs/protocol.md。
type Analysis struct {
	Squat       ActionStatus `json:"squat"`
	PushUp      ActionStatus `json:"pushup"`
	JumpingJack ActionStatus `json:"jumping_jack"`
}

// Analyzer 有状态的动作分析器，每个 WebSocket 会话独立一份。
type Analyzer struct {
	squat       actionState
	pushUp      actionState
	jumpingJack jjState
}

type actionState struct {
	count int
	stage string
}

type jjState struct {
	count int
	stage string
}

// NewAnalyzer 创建分析器，初始阶段均为 idle。
func NewAnalyzer() *Analyzer {
	return &Analyzer{
		squat:       actionState{stage: StageIdle},
		pushUp:      actionState{stage: StageIdle},
		jumpingJack: jjState{stage: StageIdle},
	}
}

// Update 输入一帧姿态，返回分析结果与提示 code 列表（可为空）。
func (a *Analyzer) Update(p pose.Pose) (Analysis, []string) {
	var tips []string
	an := Analysis{}
	an.Squat, tips = a.updateSquat(p, tips)
	an.PushUp, tips = a.updatePushUp(p, tips)
	an.JumpingJack, tips = a.updateJumpingJack(p, tips)
	return an, tips
}

// updateSquat 深蹲：膝角（髋-膝-踝）down<100° / up>160°，down→up 计一次。
func (a *Analyzer) updateSquat(p pose.Pose, tips []string) (ActionStatus, []string) {
	kneeAngle, hipAngle, ok := squatAngles(p)
	if !ok {
		return ActionStatus{Count: a.squat.count, Stage: StageIdle}, tips
	}
	stage := a.squat.stage
	switch {
	case kneeAngle < 100:
		stage = StageDown
		if hipAngle < 140 {
			tips = appendTip(tips, TipBackNotStraight)
		}
		if kneeOverToe(p) {
			tips = appendTip(tips, TipKneeOverToe)
		}
	case kneeAngle < 130:
		// 半蹲区间：已进入下蹲但深度不足，提示加深
		tips = appendTip(tips, TipSquatNotDeepEnough)
	case kneeAngle > 160:
		if stage == StageDown {
			a.squat.count++
		}
		stage = StageUp
	}
	a.squat.stage = stage
	return ActionStatus{Count: a.squat.count, Stage: stage}, tips
}

// updatePushUp 俯卧撑：肘角（肩-肘-腕）down<95° / up>160°，down→up 计一次。
func (a *Analyzer) updatePushUp(p pose.Pose, tips []string) (ActionStatus, []string) {
	elbowAngle, bodyAngle, ok := pushUpAngles(p)
	if !ok {
		return ActionStatus{Count: a.pushUp.count, Stage: StageIdle}, tips
	}
	stage := a.pushUp.stage
	switch {
	case elbowAngle < 95:
		stage = StageDown
		if bodyAngle < 150 {
			tips = appendTip(tips, TipBodyNotStraight)
		}
	case elbowAngle > 160:
		if stage == StageDown {
			a.pushUp.count++
		}
		stage = StageUp
	}
	a.pushUp.stage = stage
	return ActionStatus{Count: a.pushUp.count, Stage: stage}, tips
}

// updateJumpingJack 开合跳：open（双腕高于头+分腿）→ closed（双腕低于肩+并腿）计一次。
func (a *Analyzer) updateJumpingJack(p pose.Pose, tips []string) (ActionStatus, []string) {
	wristHigh, ankleApart, ankleTogether, ok := jackSignals(p)
	if !ok {
		return ActionStatus{Count: a.jumpingJack.count, Stage: StageIdle}, tips
	}
	stage := a.jumpingJack.stage
	switch {
	case wristHigh && ankleApart:
		stage = StageOpen
	case !wristHigh && ankleTogether:
		if stage == StageOpen {
			a.jumpingJack.count++
		}
		stage = StageClosed
	default:
		if stage == StageOpen && ankleApart {
			// 分腿已开但手臂未举到位
			tips = appendTip(tips, TipArmsNotFullyRaised)
		}
	}
	a.jumpingJack.stage = stage
	return ActionStatus{Count: a.jumpingJack.count, Stage: stage}, tips
}

// squatAngles 返回膝角与髋角（躯干-大腿），取两侧有效值的均值。
func squatAngles(p pose.Pose) (knee, hip float64, ok bool) {
	lk, ok1 := angle3(p, pose.LeftHip, pose.LeftKnee, pose.LeftAnkle)
	rk, ok2 := angle3(p, pose.RightHip, pose.RightKnee, pose.RightAnkle)
	if !ok1 && !ok2 {
		return 0, 0, false
	}
	knee = meanAngle(lk, ok1, rk, ok2)

	lh, ok3 := angle3(p, pose.LeftShoulder, pose.LeftHip, pose.LeftKnee)
	rh, ok4 := angle3(p, pose.RightShoulder, pose.RightHip, pose.RightKnee)
	hip = meanAngle(lh, ok3, rh, ok4)
	return knee, hip, true
}

// pushUpAngles 返回肘角与身体直线角（肩-髋-膝）。
func pushUpAngles(p pose.Pose) (elbow, body float64, ok bool) {
	le, ok1 := angle3(p, pose.LeftShoulder, pose.LeftElbow, pose.LeftWrist)
	re, ok2 := angle3(p, pose.RightShoulder, pose.RightElbow, pose.RightWrist)
	if !ok1 && !ok2 {
		return 0, 0, false
	}
	elbow = meanAngle(le, ok1, re, ok2)

	lb, ok3 := angle3(p, pose.LeftShoulder, pose.LeftHip, pose.LeftKnee)
	rb, ok4 := angle3(p, pose.RightShoulder, pose.RightHip, pose.RightKnee)
	body = meanAngle(lb, ok3, rb, ok4)
	return elbow, body, true
}

// jackSignals 返回开合跳三路信号。
func jackSignals(p pose.Pose) (wristHigh, ankleApart, ankleTogether, ok bool) {
	nose := p.At(pose.Nose)
	lw, rw := p.At(pose.LeftWrist), p.At(pose.RightWrist)
	la, ra := p.At(pose.LeftAnkle), p.At(pose.RightAnkle)
	ls, rs := p.At(pose.LeftShoulder), p.At(pose.RightShoulder)

	need := []pose.Keypoint{nose, lw, rw, la, ra, ls, rs}
	for _, k := range need {
		if k.Score < scoreGate {
			return false, false, false, false
		}
	}
	shoulderW := abs(ls.X - rs.X)
	spread := abs(la.X - ra.X)
	wristHigh = lw.Y < nose.Y && rw.Y < nose.Y
	ankleApart = spread > shoulderW*1.2
	ankleTogether = spread < shoulderW*0.9
	return wristHigh, ankleApart, ankleTogether, true
}

// kneeOverToe 膝盖水平偏移显著超过脚踝（粗略启发式，阈值相对肩宽）。
// 肩宽过小（侧身视角双肩几乎重合）时无法判定，直接跳过。
func kneeOverToe(p pose.Pose) bool {
	ls, rs := p.At(pose.LeftShoulder), p.At(pose.RightShoulder)
	if ls.Score < scoreGate || rs.Score < scoreGate {
		return false
	}
	shoulderW := abs(ls.X - rs.X)
	if shoulderW < 0.02 {
		return false
	}
	check := func(knee, ankle pose.Keypoint) bool {
		return knee.Score >= scoreGate && ankle.Score >= scoreGate &&
			abs(knee.X-ankle.X) > shoulderW*0.5
	}
	return check(p.At(pose.LeftKnee), p.At(pose.LeftAnkle)) ||
		check(p.At(pose.RightKnee), p.At(pose.RightAnkle))
}

// angle3 计算 a-b-c 在 b 处的夹角（度），任一点置信度不足视为无效。
func angle3(p pose.Pose, a, b, c int) (float64, bool) {
	ka, kb, kc := p.At(a), p.At(b), p.At(c)
	if ka.Score < scoreGate || kb.Score < scoreGate || kc.Score < scoreGate {
		return 0, false
	}
	v1 := [2]float64{ka.X - kb.X, ka.Y - kb.Y}
	v2 := [2]float64{kc.X - kb.X, kc.Y - kb.Y}
	dot := v1[0]*v2[0] + v1[1]*v2[1]
	n1 := math.Hypot(v1[0], v1[1])
	n2 := math.Hypot(v2[0], v2[1])
	if n1 == 0 || n2 == 0 {
		return 0, false
	}
	cos := dot / (n1 * n2)
	cos = math.Max(-1, math.Min(1, cos))
	return math.Acos(cos) * 180 / math.Pi, true
}

// meanAngle 求有效侧的均值。
func meanAngle(l float64, lok bool, r float64, rok bool) float64 {
	switch {
	case lok && rok:
		return (l + r) / 2
	case lok:
		return l
	default:
		return r
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func appendTip(tips []string, tip string) []string {
	for _, t := range tips {
		if t == tip {
			return tips
		}
	}
	return append(tips, tip)
}
