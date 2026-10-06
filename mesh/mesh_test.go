package mesh

import (
	"strings"
	"testing"
)

// 搭建链式拓扑 3 <-> 2 <-> 1(root)，验证收敛后的父指针与上行路径。
func TestConvergeChain(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 3})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	net.Run()
	net.Advance(20)

	n2, n3 := net.Get(2), net.Get(3)
	if n2.Parent() != 1 {
		t.Fatalf("n2 parent=%d want 1", n2.Parent())
	}
	if n3.Parent() != 2 {
		t.Fatalf("n3 parent=%d want 2", n3.Parent())
	}
	p := n3.UpwardPath()
	if !p.Valid() {
		t.Fatal("n3 has no upward path")
	}
	want := []NodeID{3, 2, 1}
	if len(p.Hops) != len(want) {
		t.Fatalf("n3 hops=%v want %v", p.Hops, want)
	}
	for i := range want {
		if p.Hops[i] != want[i] {
			t.Fatalf("n3 hops=%v want %v", p.Hops, want)
		}
	}
	if p.Cost != 2 {
		t.Fatalf("n3 cost=%v want 2", p.Cost)
	}
}

// 收敛后验证上行数据汇聚与下行数据下发。
func TestDataUpAndDown(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 3})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	net.Submit(Event{Time: 30, Type: EvSendUp, A: 3})
	net.Submit(Event{Time: 31, Type: EvSendDown, A: 3})
	logs := net.Run()
	net.Advance(20)

	assertLogHas(t, logs, "send-up", "result=ok")
	assertLogHas(t, logs, "send-down", "dst=3 result=ok")
	assertLogHas(t, logs, "deliver-up", "src=3")
	assertLogHas(t, logs, "deliver-down", "")
}

func assertLogHas(t *testing.T, logs []LogEntry, action, detailSub string) {
	t.Helper()
	for _, l := range logs {
		if l.Action != action {
			continue
		}
		if detailSub == "" || strings.Contains(l.Detail, detailSub) {
			return
		}
	}
	t.Fatalf("log missing action=%q detail~=%q", action, detailSub)
}

// 更优链路出现后节点应切换父节点，切换后上下行均收敛。
func TestParentSwitch(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 3})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	// t=30：出现更优直连链路 1-3，n3 应切换到 1。
	net.Submit(Event{Time: 30, Type: EvLinkChange, A: 1, B: 3, Q: 1})
	// t=40：切换完成后上下行都应可用。
	net.Submit(Event{Time: 40, Type: EvSendUp, A: 3})
	net.Submit(Event{Time: 41, Type: EvSendDown, A: 3})
	logs := net.Run()
	net.Advance(30)

	n3 := net.Get(3)
	if n3.Parent() != 1 {
		t.Fatalf("after switch n3 parent=%d want 1", n3.Parent())
	}
	p := n3.UpwardPath()
	if !p.Valid() || len(p.Hops) != 2 || p.Hops[1] != 1 {
		t.Fatalf("after switch n3 path=%v", p.Hops)
	}
	// 下行路径必须与当前上行结构一致：根的下行表直接指向 3。
	next, ok := net.Get(1).DownwardNextHop(3)
	if !ok || next != 3 {
		t.Fatalf("root downward next hop for 3 = %d,%v want 3,true", next, ok)
	}
	assertLogHas(t, logs, "send-up", "result=ok")
	assertLogHas(t, logs, "send-down", "dst=3 result=ok")
	// 切换必须留下 poison 痕迹（旧父 2 撤销指向 3 的下行路由）。
	assertLogHas(t, logs, "dao-poison", "dest=3")
}

// 父节点失效且暂无合格候选时，转发必须返回可区分原因而非静默丢弃。
func TestForwardErrorDistinguishable(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	// t=30：唯一上行链路断开，n2 成为孤儿。
	net.Submit(Event{Time: 30, Type: EvLinkChange, A: 1, B: 2, Q: 0})
	net.Submit(Event{Time: 31, Type: EvSendUp, A: 2})
	// t=32：向已离开的节点 9 发下行。
	net.Submit(Event{Time: 32, Type: EvSendDown, A: 9})
	logs := net.Run()
	net.Advance(10)

	assertLogHas(t, logs, "send-up", "result=no-upward-route")
	assertLogHas(t, logs, "send-down", "dst=9 result=destination-down")
}

// 中间节点离开后，其子树必须经备用链路重新挂接到根。
func TestNodeLeaveReattach(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 3})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	// 备用链路质量较差，仅作后备。
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 3, Q: 5})
	// t=30：节点 2 离开。
	net.Submit(Event{Time: 30, Type: EvNodeLeave, A: 2})
	net.Submit(Event{Time: 45, Type: EvSendUp, A: 3})
	net.Submit(Event{Time: 46, Type: EvSendDown, A: 3})
	logs := net.Run()
	net.Advance(20)

	n3 := net.Get(3)
	if n3.Parent() != 1 {
		t.Fatalf("after n2 leave, n3 parent=%d want 1", n3.Parent())
	}
	if p := n3.UpwardPath(); !p.Valid() || p.Cost != 5 {
		t.Fatalf("n3 path=%+v want cost 5 via backup link", p)
	}
	assertLogHas(t, logs, "send-up", "result=ok")
	assertLogHas(t, logs, "send-down", "dst=3 result=ok")
}

// 数据面环路检测：携带不合法 Rank 的上行报文必须被拒绝并触发修复。
func TestLoopDetectionOnData(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Run()
	net.Advance(20)

	n2 := net.Get(2)
	if n2.Parent() != 1 {
		t.Fatalf("n2 parent=%d want 1", n2.Parent())
	}
	// 伪造来自“更低 Rank 节点”的上行报文：违反 R1，必须判定为环。
	m := Msg{Type: MsgDataUp, From: 7, Src: 7, Dst: 1, PayloadID: 999, Rank: n2.Rank(), TTL: maxTTL}
	if r := n2.OnDataUp(m); r != ErrLoopDetected {
		t.Fatalf("OnDataUp with bad rank = %s want loop-detected", r)
	}
	// TTL 耗尽的报文同样判定为环。
	m2 := Msg{Type: MsgDataUp, From: 7, Src: 7, Dst: 1, PayloadID: 1000, Rank: Rank(100), TTL: 0}
	if r := n2.OnDataUp(m2); r != ErrLoopDetected {
		t.Fatalf("OnDataUp with ttl=0 = %s want loop-detected", r)
	}
}

// 节点重启后：代次递增、状态清空、能重新接入并恢复转发。
func TestNodeRestart(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 3})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	// t=30：节点 2 重启。
	net.Submit(Event{Time: 30, Type: EvNodeRestart, A: 2})
	// t=50：恢复后上下行应可用。
	net.Submit(Event{Time: 50, Type: EvSendUp, A: 3})
	net.Submit(Event{Time: 51, Type: EvSendDown, A: 3})
	logs := net.Run()
	net.Advance(20)

	n2 := net.Get(2)
	if n2.seq != 2 {
		t.Fatalf("n2 seq=%d want 2 (restart bumps incarnation)", n2.seq)
	}
	if n2.Parent() != 1 {
		t.Fatalf("after restart n2 parent=%d want 1", n2.Parent())
	}
	if n3 := net.Get(3); n3.Parent() != 2 {
		t.Fatalf("after restart n3 parent=%d want 2", n3.Parent())
	}
	assertLogHas(t, logs, "send-up", "result=ok")
	assertLogHas(t, logs, "send-down", "dst=3 result=ok")
}

// 同一身份的新旧化身共存时，旧代次消息必须被确定性地丢弃。
func TestStaleIncarnationRejected(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 1, IsRoot: true})
	net.Submit(Event{Time: 0, Type: EvNodeJoin, A: 2})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Run()
	net.Advance(20)

	n2 := net.Get(2)
	// 伪造一条来自“未来代次”的 DIO，使 n2 记住根的新代次。
	n2.OnDIO(Msg{Type: MsgDIO, From: 1, Version: 1, Seq: 5, Rank: RootRank})
	// 随后到达的旧代次 DIO 必须被丢弃，不得覆盖邻居表。
	n2.OnDIO(Msg{Type: MsgDIO, From: 1, Version: 1, Seq: 3, Rank: RootRank})
	nb := n2.parents[1]
	if nb.seq != 5 {
		t.Fatalf("neighbor seq=%d want 5 (stale seq 3 must not overwrite)", nb.seq)
	}
	// 旧版本拓扑的 DIO 也必须被丢弃。
	n2.OnDIO(Msg{Type: MsgDIO, From: 9, Version: 0, Seq: 1, Rank: 2})
	if _, ok := n2.parents[9]; ok {
		t.Fatal("stale-version DIO must not create neighbor entry")
	}
}

// 链路频繁抖动下，每个事件处理后拓扑都必须保持无环，
// 抖动停止后拓扑能够收敛，数据转发恢复。
func TestLinkFlapping(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	for _, id := range []NodeID{1, 2, 3, 4} {
		net.Submit(Event{Time: 0, Type: EvNodeJoin, A: id, IsRoot: id == 1})
	}
	links := [][3]uint64{{1, 2, 1}, {2, 3, 1}, {1, 3, 2}, {3, 4, 1}, {2, 4, 3}}
	for _, l := range links {
		net.Submit(Event{Time: 1, Type: EvLinkChange, A: NodeID(l[0]), B: NodeID(l[1]), Q: LinkQuality(l[2])})
	}
	// 抖动：链路 1-2 反复断开/恢复，质量来回变化。
	tm := uint64(10)
	for i := 0; i < 6; i++ {
		q := LinkQuality(0)
		if i%2 == 1 {
			q = LinkQuality(1 + i)
		}
		net.Submit(Event{Time: tm, Type: EvLinkChange, A: 1, B: 2, Q: q})
		tm += 3
	}
	net.Run()
	// 每个事件处理后立即校验无环性与状态一致性。
	assertAcyclic(t, net)
	assertStateConsistent(t, net)
	// 抖动停止后收敛。
	net.Advance(100)
	assertAcyclic(t, net)
	assertStateConsistent(t, net)
	for _, id := range []NodeID{2, 3, 4} {
		if !net.Get(id).Attached() {
			t.Fatalf("node %d not attached after convergence", id)
		}
		if p := net.Get(id).UpwardPath(); !p.Valid() {
			t.Fatalf("node %d has no valid upward path after convergence", id)
		}
	}
}

// assertAcyclic 校验全网无环：所有已接入节点的上行链都能到达根。
func assertAcyclic(t *testing.T, net *Network) {
	t.Helper()
	for _, id := range net.sortedNodeIDs() {
		nd := net.Get(id)
		if !nd.Attached() {
			continue
		}
		if p := nd.UpwardPath(); !p.Valid() {
			t.Fatalf("node %d attached but upward path invalid (loop or broken chain)", id)
		}
	}
}

// 固定随机种子与相同事件序列下，两次运行必须产生逐事件一致的日志。
func TestDeterministicReplay(t *testing.T) {
	scenario := func() []LogEntry {
		net := NewNetwork(DefaultConfig())
		for id := NodeID(1); id <= 5; id++ {
			net.Submit(Event{Time: 0, Type: EvNodeJoin, A: id, IsRoot: id == 1})
		}
		net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
		net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 2})
		net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 4, Q: 3})
		net.Submit(Event{Time: 1, Type: EvLinkChange, A: 4, B: 5, Q: 1})
		net.Submit(Event{Time: 1, Type: EvLinkChange, A: 3, B: 5, Q: 2})
		net.Submit(Event{Time: 10, Type: EvLinkChange, A: 1, B: 2, Q: 0})
		net.Submit(Event{Time: 13, Type: EvLinkChange, A: 1, B: 2, Q: 4})
		net.Submit(Event{Time: 20, Type: EvNodeRestart, A: 4})
		net.Submit(Event{Time: 30, Type: EvSendUp, A: 5})
		net.Submit(Event{Time: 31, Type: EvSendDown, A: 3})
		net.Run()
		net.Advance(50)
		return net.Logs()
	}
	a, b := scenario(), scenario()
	if len(a) != len(b) {
		t.Fatalf("log length differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("log[%d] differs:\n  %+v\n  %+v", i, a[i], b[i])
		}
	}
	if len(a) == 0 {
		t.Fatal("scenario produced no logs")
	}
}

// Trickle 自适应抑制：稳定期控制开销明显下降，拓扑变化时更新及时传播。
func TestTrickleSuppression(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	for id := NodeID(1); id <= 4; id++ {
		net.Submit(Event{Time: 0, Type: EvNodeJoin, A: id, IsRoot: id == 1})
	}
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 3, B: 4, Q: 1})
	net.Run()
	net.Advance(30) // 收敛期

	// 稳定窗口：200 tick 内的 DIO 数量（间隔已退避到 Imax，开销应很低）。
	base := len(net.Logs())
	net.Advance(200)
	stableDIO := countAction(net.Logs()[base:], "dio")

	// 拓扑变化：出现更优链路 1-3，触发 n3 换父并加速传播。
	net.SetLink(1, 3, 1)
	base = len(net.Logs())
	net.Advance(5)
	changeDIO := countAction(net.Logs()[base:], "dio")

	// 稳定期速率必须远低于变化期速率（自适应抑制生效）。
	if stableDIO == 0 {
		t.Fatal("expected some DIO in stable window (keep-alive)")
	}
	if stableDIO > 20 {
		t.Fatalf("stable-window DIO too high: %d in 200 ticks", stableDIO)
	}
	if changeDIO < 2 {
		t.Fatalf("change not propagated promptly: %d DIO in 5 ticks after change", changeDIO)
	}
	// 速率比较：变化期 5 tick 折算到 200 tick 必须显著多于稳定期。
	if changeDIO*40 <= stableDIO*2 {
		t.Fatalf("suppression ineffective: stable=%d/200t change=%d/5t", stableDIO, changeDIO)
	}
	// 变化后拓扑收敛且无环。
	net.Advance(100)
	assertAcyclic(t, net)
	if net.Get(3).Parent() != 1 {
		t.Fatalf("n3 should have switched to better parent 1, got %d", net.Get(3).Parent())
	}
}

func countAction(logs []LogEntry, action string) int {
	c := 0
	for _, l := range logs {
		if l.Action == action {
			c++
		}
	}
	return c
}

// 同一 tick 内并发产生多个拓扑事件：处理后必须保持无环，
// 且任何节点不得同时保留互相矛盾的上行/下行状态。
func TestConcurrentEvents(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	for id := NodeID(1); id <= 6; id++ {
		net.Submit(Event{Time: 0, Type: EvNodeJoin, A: id, IsRoot: id == 1})
	}
	links := []struct {
		a, b NodeID
		q    LinkQuality
	}{{1, 2, 1}, {2, 3, 1}, {3, 4, 1}, {1, 5, 2}, {5, 6, 1}, {2, 6, 2}, {4, 6, 4}}
	for _, l := range links {
		net.Submit(Event{Time: 1, Type: EvLinkChange, A: l.a, B: l.b, Q: l.q})
	}
	// t=20：四个事件并发——断链、新链、重启、质量变化。
	net.Submit(Event{Time: 20, Type: EvLinkChange, A: 1, B: 2, Q: 0})
	net.Submit(Event{Time: 20, Type: EvLinkChange, A: 3, B: 5, Q: 1})
	net.Submit(Event{Time: 20, Type: EvNodeRestart, A: 4})
	net.Submit(Event{Time: 20, Type: EvLinkChange, A: 2, B: 3, Q: 8})
	// t=23：再次并发——节点离开与链路恢复同时发生。
	net.Submit(Event{Time: 23, Type: EvNodeLeave, A: 6})
	net.Submit(Event{Time: 23, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 40, Type: EvSendUp, A: 3})
	net.Submit(Event{Time: 41, Type: EvSendDown, A: 3})
	logs := net.Run()
	assertAcyclic(t, net)
	assertStateConsistent(t, net)
	net.Advance(100)
	assertAcyclic(t, net)
	assertStateConsistent(t, net)
	assertLogHas(t, logs, "send-up", "result=ok")
	assertLogHas(t, logs, "send-down", "dst=3 result=ok")
}

// assertStateConsistent 校验每个节点的上行与下行状态不自相矛盾：
//   - 父节点不得出现在本节点的下行路由表（子孙）中；
//   - 已接入非根节点的 Rank 必须严格大于其父的 Rank（R1）；
//   - 下行路由的下一跳必须是当前可达邻居。
func assertStateConsistent(t *testing.T, net *Network) {
	t.Helper()
	for _, id := range net.sortedNodeIDs() {
		nd := net.Get(id)
		if nd.parent != InvalidNodeID {
			if _, conflict := nd.down[nd.parent]; conflict {
				t.Fatalf("node %d: parent %d also appears as descendant", id, nd.parent)
			}
			if p, ok := nd.parents[nd.parent]; ok && p.rank+1 > nd.rank {
				t.Fatalf("node %d: rank %d not greater than parent %d rank %d", id, nd.rank, nd.parent, p.rank)
			}
		}
		for dest, rt := range nd.down {
			if net.Link(id, rt.nextHop) <= LinkDown {
				t.Fatalf("node %d: down route to %d via unreachable neighbor %d", id, dest, rt.nextHop)
			}
		}
	}
}

// 下行路径必须与当前上行结构一致：从根沿下行表到达任意节点的路径，
// 必须恰好是该节点上行路径的逆。结构变化（换父）后该性质在有限时间内恢复。
func TestDownwardConsistentWithUpward(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	for id := NodeID(1); id <= 5; id++ {
		net.Submit(Event{Time: 0, Type: EvNodeJoin, A: id, IsRoot: id == 1})
	}
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 3, B: 4, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 5, Q: 2})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 5, B: 4, Q: 3})
	// t=30：拓扑变化，触发换父。
	net.Submit(Event{Time: 30, Type: EvLinkChange, A: 2, B: 3, Q: 0})
	net.Submit(Event{Time: 30, Type: EvLinkChange, A: 1, B: 3, Q: 2})
	net.Run()
	net.Advance(60) // 有限时间收敛窗口

	assertDownwardPaths(t, net)
}

// assertDownwardPaths 校验每个节点的下行路径是其上行路径的逆。
func assertDownwardPaths(t *testing.T, net *Network) {
	t.Helper()
	root := net.Get(net.Root())
	if root == nil {
		t.Fatal("no root")
	}
	for _, id := range net.sortedNodeIDs() {
		nd := net.Get(id)
		if nd == root || !nd.Attached() {
			continue
		}
		up := nd.UpwardPath()
		if !up.Valid() {
			t.Fatalf("node %d: no upward path", id)
		}
		// 从根沿下行表走，收集路径。
		var downHops []NodeID
		cur := root
		guard := 0
		for cur.ID() != id {
			downHops = append(downHops, cur.ID())
			next, ok := cur.DownwardNextHop(id)
			if !ok {
				t.Fatalf("node %d: downward path broken at %d", id, cur.ID())
			}
			cur = net.Get(next)
			if cur == nil {
				t.Fatalf("node %d: downward next hop %d absent", id, next)
			}
			if guard++; guard > 64 {
				t.Fatalf("node %d: downward walk does not terminate (loop)", id)
			}
		}
		downHops = append(downHops, id)
		// 下行路径必须等于上行路径的逆。
		if len(downHops) != len(up.Hops) {
			t.Fatalf("node %d: down=%v up=%v length mismatch", id, downHops, up.Hops)
		}
		for i := range downHops {
			if downHops[i] != up.Hops[len(up.Hops)-1-i] {
				t.Fatalf("node %d: down=%v not reverse of up=%v", id, downHops, up.Hops)
			}
		}
	}
}

// 根节点重启后拓扑版本递增，全网旧版本状态被识别并重建。
func TestRootRestartVersionBumps(t *testing.T) {
	net := NewNetwork(DefaultConfig())
	for id := NodeID(1); id <= 3; id++ {
		net.Submit(Event{Time: 0, Type: EvNodeJoin, A: id, IsRoot: id == 1})
	}
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(Event{Time: 1, Type: EvLinkChange, A: 2, B: 3, Q: 1})
	// t=30：根重启，拓扑版本 1 -> 2。
	net.Submit(Event{Time: 30, Type: EvNodeRestart, A: 1})
	net.Submit(Event{Time: 60, Type: EvSendUp, A: 3})
	net.Submit(Event{Time: 61, Type: EvSendDown, A: 3})
	logs := net.Run()
	net.Advance(40)

	root := net.Get(1)
	if root.version != 2 {
		t.Fatalf("root version=%d want 2 after restart", root.version)
	}
	// 全网必须迁移到新版本并重新收敛。
	for _, id := range []NodeID{2, 3} {
		nd := net.Get(id)
		if nd.version != 2 {
			t.Fatalf("node %d version=%d want 2", id, nd.version)
		}
		if !nd.Attached() {
			t.Fatalf("node %d not attached after root restart", id)
		}
	}
	assertAcyclic(t, net)
	assertStateConsistent(t, net)
	assertLogHas(t, logs, "version-reset", "")
	assertLogHas(t, logs, "send-up", "result=ok")
	assertLogHas(t, logs, "send-down", "dst=3 result=ok")
}
