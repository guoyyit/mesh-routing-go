package mesh

import "testing"

// 稳定时间隔指数翻倍且不超过 Imax，控制开销随之下降。
func TestTrickleBackoff(t *testing.T) {
	tr := NewTrickle(2, 8, 100, nil) // K 很大：不抑制
	fires := 0
	// 窗口 2+4+8+8 = 22 tick 内，每窗口恰好发送一次。
	for i := uint64(0); i < 22; i++ {
		if tr.Tick() {
			fires++
		}
	}
	if fires != 4 {
		t.Fatalf("fires=%d want 4 (intervals 2,4,8,8)", fires)
	}
	if tr.i != 8 {
		t.Fatalf("interval=%d want capped at Imax=8", tr.i)
	}
}

// 冗余消息达到 K 后发送被抑制；但重要变化（Reset）必须打破抑制。
func TestTrickleSuppressionAndReset(t *testing.T) {
	tr := NewTrickle(4, 64, 1, nil)
	tr.Consistent() // c=1 >= K=1，本窗口发送被抑制
	fired := false
	for i := uint64(0); i < 4; i++ {
		if tr.Tick() {
			fired = true
		}
	}
	if fired {
		t.Fatal("send should be suppressed after K consistent messages")
	}
	// 抑制窗口内发生重要变化：Reset 后新窗口必须发送（变化不被吞掉）。
	tr.Reset()
	fired = false
	for i := uint64(0); i < 4; i++ {
		if tr.Tick() {
			fired = true
		}
	}
	if !fired {
		t.Fatal("reset must break suppression and schedule a send")
	}
}
