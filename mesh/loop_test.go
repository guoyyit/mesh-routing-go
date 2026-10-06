package mesh

import "testing"

// 环路检测：手工构造父指针环，CheckAcyclic 必须识别并给出环节点。
func TestCycleDetection(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	NewSimulator(net, joinChain()).Run()
	if ok, _ := net.CheckAcyclic(); !ok {
		t.Fatal("unexpected cycle in healthy chain")
	}

	// 包内篡改：制造 2 -> 3 -> 2 的环（3 的父本是 2，将 2 的父改为 3）。
	n2, n3 := net.Node(2), net.Node(3)
	n2.parent, n2.hasPar = 3, true
	n2.Rank = n3.Rank + 256

	ok, cycle := net.CheckAcyclic()
	if ok {
		t.Fatal("cycle not detected")
	}
	if len(cycle) < 2 {
		t.Fatalf("cycle list too short: %v", cycle)
	}
}

// 无环不变量实时校验：父节点通告 Rank 不再严格小于本节点时，
// ForwardUp 拒绝转发并返回 loop-detected，而不是静默丢弃或成环转发。
func TestForwardRejectsBrokenInvariant(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	NewSimulator(net, joinChain()).Run()

	n3 := net.Node(3)
	// 篡改父节点 2 在 3 的邻居表中的通告 Rank，破坏递减不变量。
	n3.NeighborOf(2).Rank = n3.Rank + 1
	if _, err := n3.ForwardUp(); err == nil || err.Reason != FwdLoopDetected {
		t.Fatalf("forward with broken invariant = %v, want loop-detected", err)
	}
}

// 选父防环规则：节点绝不选择下行表中的后代作为父节点。
// 构造场景：1-2-3 链，随后 1-3 出现高质量直连链路。
// 若 2 允许选 3 为父将构成 2->3->2 环（3 是 2 的后代）。
func TestNeverSelectDescendantAsParent(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	NewSimulator(net, []Event{
		{Kind: EvNodeJoin, A: 1},
		{Kind: EvNodeJoin, A: 2},
		{Kind: EvNodeJoin, A: 3},
		{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
		{Kind: EvLinkUp, A: 2, B: 3, Cost: 256},
		// 1-2 劣化，同时出现 2-3 之外的唯一替代：无。2 的唯一父仍是 1。
		// 现在让 1-2 断开：2 只剩邻居 3（其后代），不得选 3 为父。
		{Kind: EvLinkDown, A: 1, B: 2},
	}).Run()

	n2 := net.Node(2)
	if p, ok := n2.Parent(); ok {
		t.Fatalf("node2 selected parent %d, but only descendant 3 is available", p)
	}
	if _, err := n2.ForwardUp(); err == nil || err.Reason != FwdNoRoute {
		t.Fatalf("node2 forward = %v, want no-route", err)
	}
	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle formed: %v", cycle)
	}
}
