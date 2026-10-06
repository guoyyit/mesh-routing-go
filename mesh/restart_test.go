package mesh

import "testing"

func joinChain() []Event {
	return []Event{
		{Kind: EvNodeJoin, A: 1},
		{Kind: EvNodeJoin, A: 2},
		{Kind: EvNodeJoin, A: 3},
		{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
		{Kind: EvLinkUp, A: 2, B: 3, Cost: 256},
	}
}

// 叶节点重启后以新化身重新接入，下行表恢复，陈旧状态不混用。
func TestLeafRestartRejoins(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	evs := append(joinChain(), Event{Kind: EvNodeRestart, A: 3})
	NewSimulator(net, evs).Run()

	n3 := net.Node(3)
	if n3.Seq != 1 {
		t.Fatalf("node3 seq = %d, want 1 after restart", n3.Seq)
	}
	if !net.Converged() {
		t.Fatal("not converged after restart")
	}
	if p, _ := n3.Parent(); p != 2 {
		t.Fatalf("node3 parent = %v, want 2", p)
	}
	// 根的下行表重新学到 3，且代价与新 Rank 一致。
	if e := net.Node(1).Down()[3]; e.nextHop != 2 || e.cost != n3.Rank {
		t.Fatalf("root down entry for 3 = %+v, want nextHop=2 cost=%d", e, n3.Rank)
	}
	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle after restart: %v", cycle)
	}
}

// 中间节点重启：其子树随之一同恢复。
func TestMiddleNodeRestart(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	evs := append(joinChain(), Event{Kind: EvNodeRestart, A: 2})
	NewSimulator(net, evs).Run()

	if !net.Converged() {
		t.Fatal("not converged after middle restart")
	}
	if p, _ := net.Node(2).Parent(); p != 1 {
		t.Fatalf("node2 parent = %v, want 1", p)
	}
	if p, _ := net.Node(3).Parent(); p != 2 {
		t.Fatalf("node3 parent = %v, want 2", p)
	}
	if e := net.Node(1).Down()[3]; e.nextHop != 2 {
		t.Fatalf("root down nextHop for 3 = %v, want 2", e.nextHop)
	}
}

// 根重启提升 DODAG 版本，全网迁移到新版本。
func TestRootRestartBumpsVersion(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	evs := append(joinChain(), Event{Kind: EvNodeRestart, A: 1})
	NewSimulator(net, evs).Run()

	if !net.Converged() {
		t.Fatal("not converged after root restart")
	}
	for _, id := range net.Nodes() {
		if v := net.Node(id).Version; v != 2 {
			t.Fatalf("node %d version = %d, want 2", id, v)
		}
	}
	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle after root restart: %v", cycle)
	}
}

// 相同 ID 重复加入按重启处理，结果确定：两次运行的逐事件日志一致。
func TestDuplicateJoinDeterministic(t *testing.T) {
	run := func() []LogEntry {
		net := NewNetwork(1, 42, 1, 8, 1)
		evs := append(joinChain(), Event{Kind: EvNodeJoin, A: 3}) // 重复身份
		return NewSimulator(net, evs).Run()
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("log length differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("log %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
	// 重复加入等价于重启：节点 3 的化身序列号递增。
	net := NewNetwork(1, 42, 1, 8, 1)
	evs := append(joinChain(), Event{Kind: EvNodeJoin, A: 3})
	NewSimulator(net, evs).Run()
	if s := net.Node(3).Seq; s != 1 {
		t.Fatalf("duplicate join should act as restart, seq = %d, want 1", s)
	}
}

// 重启节点的陈旧通告（旧 Seq）被邻居拒绝。
func TestStaleIncarnationRejected(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	evs := append(joinChain(), Event{Kind: EvNodeRestart, A: 3})
	NewSimulator(net, evs).Run()

	n2 := net.Node(2)
	if s := n2.NeighborOf(3).Seq; s != 1 {
		t.Fatalf("neighbor seq = %d, want 1 after node3 restart", s)
	}
	before := n2.NeighborOf(3).Rank
	// 伪造一条来自 3 的旧化身 DIO（seq=0 小于当前记录的 1）。
	changed := n2.ReceiveDIO(3, 1, 0, 999, 1, net.Time())
	if changed {
		t.Fatal("stale DIO should be ignored")
	}
	if got := n2.NeighborOf(3).Rank; got != before {
		t.Fatalf("stale DIO mutated neighbor rank: %d -> %d", before, got)
	}
}
