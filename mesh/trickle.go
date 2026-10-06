package mesh

import "math/rand"

// Trickle 实现 RFC 6206 风格的自适应控制消息抑制：
//   - 拓扑稳定时，发送间隔按指数回退（I 翻倍直至 Imax），控制开销明显下降；
//   - 收到不一致信息（拓扑变化）时立即重置到最小窗口，保证更新及时传播；
//   - 窗口内收到 K 条一致消息则抑制本次发送，但 Inconsistent 触发的重置
//     不受抑制窗口影响，重要变化不会被吞掉。
//
// 随机性由调用方注入的 *rand.Rand 提供，配合固定种子可逐事件复现。
type Trickle struct {
	Imin uint64 // 最小窗口（时钟步）
	Imax uint64 // 最大窗口（时钟步）
	K    int    // 冗余常数：窗口内收到 K 条一致消息则抑制本次发送

	rng   *rand.Rand // 节点级确定性随机源
	now   uint64     // 当前时钟
	start uint64     // 当前窗口起点
	I     uint64     // 当前窗口长度
	t     uint64     // 相对窗口起点的发送偏移
	c     int        // 当前窗口内收到的一致消息计数
	sent  bool       // 当前窗口的发送时刻是否已过
}

// NewTrickle 创建一个抑制器并立即进入首个最小窗口。
func NewTrickle(imin, imax uint64, k int, rng *rand.Rand) *Trickle {
	if imin == 0 {
		imin = 1
	}
	if imax < imin {
		imax = imin
	}
	if k <= 0 {
		k = 1
	}
	t := &Trickle{Imin: imin, Imax: imax, K: k, rng: rng}
	t.Reset()
	return t
}

// pick 在新窗口内随机选择发送时刻，区间为 [I/2, I)。
func (t *Trickle) pick() {
	half := t.I / 2
	if half == 0 {
		t.t = 0
		return
	}
	t.t = half + uint64(t.rng.Int63n(int64(half)))
}

// Reset 因本地拓扑变化或收到不一致信息而重置到最小窗口，
// 下一发送时刻被立即重新安排，保证变化及时传播。
func (t *Trickle) Reset() {
	t.I = t.Imin
	t.start = t.now
	t.c = 0
	t.sent = false
	t.pick()
}

// Consistent 记录收到一条与本地状态一致的控制消息。
func (t *Trickle) Consistent() {
	t.c++
}

// Inconsistent 记录收到一条不一致消息，立即重置窗口。
// 该重置不经过抑制判断，因此抑制窗口内的重要变化不会被吞掉。
func (t *Trickle) Inconsistent() {
	t.Reset()
}

// Advance 推进时钟 steps 步；返回 true 表示推进过程中到达过发送时刻
// 且未被冗余抑制，调用方应发送一次控制消息。
func (t *Trickle) Advance(steps uint64) bool {
	t.now += steps
	send := false
	for {
		// 发送时刻到达：根据冗余常数决定是否真正发送。
		if !t.sent && t.now >= t.start+t.t {
			t.sent = true
			if t.c < t.K {
				send = true
			}
		}
		// 窗口结束：指数回退并开启新窗口。
		if t.now >= t.start+t.I {
			t.start += t.I
			t.I *= 2
			if t.I > t.Imax {
				t.I = t.Imax
			}
			t.c = 0
			t.sent = false
			t.pick()
			continue
		}
		return send
	}
}
