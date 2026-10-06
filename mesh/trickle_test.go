package mesh

import (
	"math/rand"
	"testing"
)

// 拓扑稳定时窗口指数回退，控制开销明显下降。
func TestTrickleBackoff(t *testing.T) {
	tr := NewTrickle(1, 64, 1, rand.New(rand.NewSource(1)))
	sends := 0
	for i := 0; i < 200; i++ {
		if tr.Advance(1) {
			sends++
		}
	}
	// 窗口按 1,2,4,...,64 回退，200 步内发送次数应为对数级。
	if sends == 0 || sends > 12 {
		t.Fatalf("sends in 200 stable steps = %d, want 1..12", sends)
	}
}

// 不一致事件立即重置窗口，重要变化不会被抑制窗口吞掉。
func TestTrickleInconsistentResets(t *testing.T) {
	tr := NewTrickle(1, 64, 1, rand.New(rand.NewSource(1)))
	// 推进到大窗口。
	for i := 0; i < 100; i++ {
		tr.Advance(1)
	}
	if tr.I < 16 {
		t.Fatalf("window should have backed off, I=%d", tr.I)
	}
	tr.Inconsistent()
	if tr.I != 1 {
		t.Fatalf("inconsistent did not reset window, I=%d", tr.I)
	}
	// 最小窗口内下一步即发送。
	if !tr.Advance(1) {
		t.Fatal("expected immediate send after reset")
	}
}

// 窗口内收到 K 条一致消息则抑制本次发送。
func TestTrickleSuppression(t *testing.T) {
	tr := NewTrickle(1, 4, 1, rand.New(rand.NewSource(1)))
	tr.Consistent() // c=1 >= K=1
	if tr.Advance(1) {
		t.Fatal("send should be suppressed by redundancy constant")
	}
}

// 被抑制的窗口内发生不一致，重置后仍能发送（重要变化不被吞）。
func TestTrickleSuppressedWindowStillPropagates(t *testing.T) {
	tr := NewTrickle(1, 4, 1, rand.New(rand.NewSource(1)))
	tr.Consistent()
	if tr.Advance(1) {
		t.Fatal("send should be suppressed")
	}
	tr.Inconsistent() // 窗口内发生重大变化
	if !tr.Advance(1) {
		t.Fatal("important change swallowed by suppression window")
	}
}

// 网络级：稳定期 DIO 控制开销明显低于变化期。
func TestNetworkControlOverheadAdapts(t *testing.T) {
	net := NewNetwork(1, 42, 1, 64, 1)
	NewSimulator(net, joinChain()).Run()

	// 稳定期推进 200 个时钟步。
	stableStart := net.DIOSent()
	NewSimulator(net, []Event{{Kind: EvTick, Steps: 200}}).Run()
	stableDIO := net.DIOSent() - stableStart

	// 变化期：链路抖动一次后推进 200 步。
	changeStart := net.DIOSent()
	NewSimulator(net, []Event{
		{Kind: EvLinkDown, A: 1, B: 2},
		{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
		{Kind: EvTick, Steps: 200},
	}).Run()
	changeDIO := net.DIOSent() - changeStart

	if stableDIO == 0 {
		t.Fatal("no DIO at all in stable period; trickle broken")
	}
	if changeDIO <= stableDIO {
		t.Fatalf("change period DIO (%d) should exceed stable period (%d)", changeDIO, stableDIO)
	}
	t.Logf("stable DIO=%d, change DIO=%d", stableDIO, changeDIO)
}
