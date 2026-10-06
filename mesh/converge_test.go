package mesh

import "testing"

// 链式拓扑：1(root) — 2 — 3，验证拓扑收敛、上下行路径与代价。
func TestChainConverges(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	sim := NewSimulator(net, []Event{
		{Kind: EvNodeJoin, A: 1},
		{Kind: EvNodeJoin, A: 2},
		{Kind: EvNodeJoin, A: 3},
		{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
		{Kind: EvLinkUp, A: 2, B: 3, Cost: 256},
	})
	sim.Run()

	if !net.Converged() {
		t.Fatal("network not converged")
	}
	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle detected: %v", cycle)
	}

	// 上行路径与代价
	n2, n3 := net.Node(2), net.Node(3)
	if p, ok := n2.Parent(); !ok || p != 1 {
		t.Fatalf("node2 parent = %v,%v, want 1,true", p, ok)
	}
	if p, ok := n3.Parent(); !ok || p != 2 {
		t.Fatalf("node3 parent = %v,%v, want 2,true", p, ok)
	}
	pi, err := n3.PathToRoot(net)
	if err != nil {
		t.Fatalf("node3 path: %v", err)
	}
	if pi.NextHop != 2 || pi.Cost != 512 || pi.Hops != 2 {
		t.Fatalf("node3 path = %+v, want {NextHop:2 Cost:512 Hops:2}", pi)
	}

	// 下行路径与上行结构一致
	next, err := net.Node(1).ForwardDown(3)
	if err != nil || next != 2 {
		t.Fatalf("root down to 3 = %v,%v, want 2,nil", next, err)
	}
	next, err = n2.ForwardDown(3)
	if err != nil || next != 3 {
		t.Fatalf("node2 down to 3 = %v,%v, want 3,nil", next, err)
	}
	// 根到 3 的下行代价应等于 3 的上行 Rank
	if e := net.Node(1).Down()[3]; e.cost != n3.Rank {
		t.Fatalf("down cost %d != rank %d", e.cost, n3.Rank)
	}
}

// 星形拓扑：多条等价路径时选父结果确定（代价相同取 ID 较小者）。
func TestDeterministicParentChoice(t *testing.T) {
	build := func() *Network {
		net := NewNetwork(1, 7, 1, 8, 1)
		NewSimulator(net, []Event{
			{Kind: EvNodeJoin, A: 1},
			{Kind: EvNodeJoin, A: 2},
			{Kind: EvNodeJoin, A: 3},
			{Kind: EvNodeJoin, A: 4},
			{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
			{Kind: EvLinkUp, A: 1, B: 3, Cost: 256},
			{Kind: EvLinkUp, A: 2, B: 4, Cost: 256},
			{Kind: EvLinkUp, A: 3, B: 4, Cost: 256},
		}).Run()
		return net
	}
	a, b := build(), build()
	for _, id := range []NodeID{2, 3, 4} {
		pa, _ := a.Node(id).Parent()
		pb, _ := b.Node(id).Parent()
		if pa != pb || a.Node(id).Rank != b.Node(id).Rank {
			t.Fatalf("node %d diverged: (%v,%d) vs (%v,%d)", id,
				pa, a.Node(id).Rank, pb, b.Node(id).Rank)
		}
	}
	// 4 的两条路径代价相同，应选 ID 较小的 2。
	if p, _ := a.Node(4).Parent(); p != 2 {
		t.Fatalf("node4 parent = %v, want 2", p)
	}
}

// 节点离开后，其余节点仍收敛且下行表不再含离开节点。
func TestNodeLeave(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	NewSimulator(net, []Event{
		{Kind: EvNodeJoin, A: 1},
		{Kind: EvNodeJoin, A: 2},
		{Kind: EvNodeJoin, A: 3},
		{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
		{Kind: EvLinkUp, A: 2, B: 3, Cost: 256},
		{Kind: EvNodeLeave, A: 3},
	}).Run()

	if !net.Converged() {
		t.Fatal("not converged after leave")
	}
	if _, ok := net.Node(1).Down()[3]; ok {
		t.Fatal("root still has down entry for departed node 3")
	}
	if _, ok := net.Node(2).Down()[3]; ok {
		t.Fatal("node2 still has down entry for departed node 3")
	}
}
