package mesh

import "math/rand"

// Trickle 实现 RFC 6206 风格的 Trickle 定时器，用于控制消息的自适应抑制：
//   - 拓扑稳定时，发送间隔指数增长（Imin -> Imax），控制开销明显下降；
//   - 检测到不一致（拓扑变化）时，间隔立即重置为 Imin，更新及时传播；
//   - 每个间隔内收到 K 个及以上一致消息时，抑制本次发送；
//   - 重要变化（不一致事件）会重置窗口，不会被抑制窗口吞掉。
//
// 发送时刻在 [I/2, I) 内随机选择以去同步；随机源由外部注入，
// 固定种子下行为完全可复现。
type Trickle struct {
	Imin uint64 // 最小间隔（tick 数）
	Imax uint64 // 最大间隔（tick 数）
	K    int    // 冗余抑制常数

	i   uint64 // 当前间隔
	t   uint64 // 当前间隔内的计划发送时刻
	c   int    // 当前间隔内收到的一致消息计数
	now uint64 // 当前间隔内已流逝的时间
	rng *rand.Rand
}

// NewTrickle 创建定时器；参数非法时回退到默认值。
// rng 为 nil 时使用固定种子，保证行为确定。
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
	if rng == nil {
		rng = rand.New(rand.NewSource(0))
	}
	tr := &Trickle{Imin: imin, Imax: imax, K: k, rng: rng}
	tr.Reset()
	return tr
}

// Reset 在不一致事件（拓扑变化）时调用：间隔重置为 Imin，更新立即进入快通道。
func (tr *Trickle) Reset() {
	tr.i = tr.Imin
	tr.now = 0
	tr.c = 0
	tr.t = tr.pickT()
}

// Consistent 记录一次“一致”事件（收到与本地状态一致的通告）。
// 达到 K 后本次发送被抑制，但间隔照常翻倍，实现稳定期降开销。
func (tr *Trickle) Consistent() {
	tr.c++
}

// Tick 推进一个时间片。返回 true 表示本 tick 应发送控制消息。
func (tr *Trickle) Tick() bool {
	tr.now++
	fire := tr.now == tr.t && tr.c < tr.K
	if tr.now >= tr.i {
		// 间隔结束：翻倍（上限 Imax），进入下一间隔。
		tr.i *= 2
		if tr.i > tr.Imax {
			tr.i = tr.Imax
		}
		tr.now = 0
		tr.c = 0
		tr.t = tr.pickT()
	}
	return fire
}

// pickT 在 [I/2, I) 内均匀随机选择发送时刻。
func (tr *Trickle) pickT() uint64 {
	half := tr.i / 2
	if half == 0 {
		half = 1
	}
	span := tr.i - half
	if span <= 1 {
		return half
	}
	return half + uint64(tr.rng.Int63n(int64(span)))
}
