package main

// 「这笔钱该不该收」的判断逻辑测试。
//
// decidePrintAction 是整个计费系统里唯一决定「已付的钱要不要退」的地方，
// 它错了只有两种后果：白送钱（该退的没退）或用户付了钱拿不到东西。
// 所以每个分支都要有测试盯着，而且必须能在没有 CUPS、没有打印机的
// 情况下跑 —— 这正是把它抽成纯函数的原因。

import (
	"testing"
	"time"
)

func TestDecidePrintAction(t *testing.T) {
	stuck := printStuckAfter
	wayPast := orderStuckPrintingAfter + time.Minute
	// 「有进展」= 印数刚刚还在涨。
	const fresh = time.Duration(0)

	cases := []struct {
		name          string
		state         int
		found         bool
		stuckFor      time.Duration
		noProgressFor time.Duration
		want          printAction
	}{
		// ── 真正打完了 ──
		{
			name:  "completed → 落定并收钱",
			state: cupsJobCompleted, found: true, stuckFor: time.Minute,
			want: printActionDone,
		},

		// ── 确定没打出来，可以放心退 ──
		{
			name:  "canceled → 退款",
			state: cupsJobCanceled, found: true, stuckFor: time.Minute,
			want: printActionRefunded,
		},
		{
			name:  "aborted → 退款",
			state: cupsJobAborted, found: true, stuckFor: time.Minute,
			want: printActionRefunded,
		},

		// ── processing：在出纸还是在空转，看印数动不动 ──
		{
			name:  "processing 且印数刚涨过 → 等",
			state: cupsJobProcessing, found: true, stuckFor: 5 * time.Second,
			noProgressFor: fresh,
			want:          printActionWait,
		},
		{
			// ⚠️ 对应「用户要打 6 分钟」的诉求：只要印数在涨就是真的在出纸，
			// 等多久都不能取消。
			name:  "processing 且印数一直在涨，打了很久 → 仍然等",
			state: cupsJobProcessing, found: true, stuckFor: wayPast,
			noProgressFor: fresh,
			want:          printActionWait,
		},
		{
			// ⚠️ 对应「打印机连不上」的真实场景：CUPS 报的是
			// job-state=processing + job-state-reasons=[job-printing]，
			// 状态词完全正常，只有印数停着不动 —— 那才是判据。
			name:  "processing 但印数停住不动超过阈值 → 取消并退款",
			state: cupsJobProcessing, found: true, stuckFor: stuck,
			noProgressFor: stuck,
			want:          printActionCancelRefund,
		},
		{
			name:  "processing 印数刚停，还没到阈值 → 再等等",
			state: cupsJobProcessing, found: true, stuckFor: stuck,
			noProgressFor: stuck - time.Second,
			want:          printActionWait,
		},

		// ── 排队中：等多久都是正常的 ──
		{
			name:  "pending 刚开始 → 等",
			state: cupsJobPending, found: true, stuckFor: 10 * time.Second,
			want: printActionWait,
		},
		{
			// ⚠️ 本条是这次修改的核心。前面排了别的任务时，本任务的印数
			// 当然不动 —— 但那是「在排队」，不是「卡死」。
			// 早先按时间一刀切会把这类任务取消掉：用户付了钱、纸没出来、
			// 还被告知「已退款」，而他只需要多等一会儿。
			name:  "pending 排长队很久，印数一直没动 → 仍然等，不取消",
			state: cupsJobPending, found: true, stuckFor: wayPast,
			noProgressFor: wayPast,
			want:          printActionWait,
		},
		{
			name:  "stopped 同理 → 等",
			state: cupsJobProcessingStop, found: true, stuckFor: wayPast,
			noProgressFor: wayPast,
			want:          printActionWait,
		},
		{
			name:  "pending-held 同理 → 等",
			state: cupsJobPendingHeld, found: true, stuckFor: wayPast,
			noProgressFor: wayPast,
			want:          printActionWait,
		},

		// ── 任务查不到：无从判断，不自动退款 ──
		{
			name:  "任务不见了但还很新 → 再等等",
			state: 0, found: false, stuckFor: time.Minute,
			want: printActionWait,
		},
		{
			// 这条同样要紧：查不到任务时【不退款】。
			// cupsd 清掉记录最常见的原因就是任务已经打完了 ——
			// 自动退款会变成「纸出来了、钱也退了」。
			name:  "任务不见了且很久 → 转人工，不退款",
			state: 0, found: false, stuckFor: wayPast,
			want: printActionUnconfirmed,
		},

		// ── 不认识的数值不要瞎猜 ──
		{
			name:  "未知状态 → 只记日志，不动钱",
			state: 42, found: true, stuckFor: wayPast,
			want: printActionUnknownState,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decidePrintAction(c.state, c.found, c.stuckFor, c.noProgressFor)
			if got != c.want {
				t.Errorf("decidePrintAction(state=%d, found=%v, stuckFor=%v, noProgressFor=%v) = %d，期望 %d",
					c.state, c.found, c.stuckFor, c.noProgressFor, got, c.want)
			}
		})
	}
}

// TestDecidePrintAction_LongJobIsNeverRefunded 锁定用户明确提出的一条：
//
//	「万一一个用户需要打印的很多需要打 6 分钟，那么肯定不能退款」
//
// 只要任务在 processing 且印数还在增长，等多久都不能动它。
func TestDecidePrintAction_LongJobIsNeverRefunded(t *testing.T) {
	for _, d := range []time.Duration{
		time.Minute, 6 * time.Minute, printStuckAfter,
		orderStuckPrintingAfter, 24 * time.Hour,
	} {
		if got := decidePrintAction(cupsJobProcessing, true, d, 0); got != printActionWait {
			t.Errorf("processing 印数持续增长、已打 %v，却得到 action=%d；长任务绝不能被退款", d, got)
		}
	}
}

// TestDecidePrintAction_QueuedIsNeverCancelled 锁定另一条：
// 排队中的任务印数当然不动，但那是「前面有任务」，不是「卡死」。
func TestDecidePrintAction_QueuedIsNeverCancelled(t *testing.T) {
	for _, state := range []int{cupsJobPending, cupsJobPendingHeld, cupsJobProcessingStop} {
		for _, noProgress := range []time.Duration{
			time.Minute, printStuckAfter, orderStuckPrintingAfter, 24 * time.Hour,
		} {
			if got := decidePrintAction(state, true, 24*time.Hour, noProgress); got != printActionWait {
				t.Errorf("state=%d 排队中（印数未动 %v）却被取消；排队不是卡死",
					state, noProgress)
			}
		}
	}
}

// TestProgressTracker 覆盖「印数有没有在涨」的记账逻辑。
//
// 它是卡死判据的唯一输入，算错了会直接导致误退款或永不退款。
func TestProgressTracker(t *testing.T) {
	base := time.Now()
	tr := newProgressTracker()

	// 首次观测：还不知道它会不会动，先给一个周期（返回 0）。
	if got := tr.observe("j1", 0, base); got != 0 {
		t.Errorf("首次观测应返回 0，实际 %v", got)
	}
	// 过了 1 分钟印数还没动 → 停住 1 分钟。
	if got := tr.observe("j1", 0, base.Add(time.Minute)); got != time.Minute {
		t.Errorf("印数未动应累计时长，实际 %v", got)
	}
	// 印数涨了 → 计时归零。
	if got := tr.observe("j1", 3, base.Add(2*time.Minute)); got != 0 {
		t.Errorf("印数增长后应归零，实际 %v", got)
	}
	// 再停住 3 分钟 → 应当正好累计到阈值。
	if got := tr.observe("j1", 3, base.Add(5*time.Minute)); got != 3*time.Minute {
		t.Errorf("应累计到 3 分钟，实际 %v", got)
	}

	// 不同任务互不干扰 —— 否则并发打印会互相把对方的计时归零。
	if got := tr.observe("j2", 0, base.Add(5*time.Minute)); got != 0 {
		t.Errorf("新任务应独立计时，实际 %v", got)
	}
}
