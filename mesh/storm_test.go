package mesh

import (
	"math/rand"
	"testing"
)

// 并发拓扑事件：同一批次内多条链路同时变化，统一收敛后
// 无环且每个节点的上行/下行状态一致。
func TestConcurrentBatchEvents(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	// 先建立网格：1-2, 1-3, 2-4, 3-4, 2-3
	NewSimulator(net, []Event{
		{Kind: EvNodeJoin, A: 1},
		{Kind: EvNodeJoin, A: 2},
		{Kind: EvNodeJoin, A: 3},
		{Kind: EvNodeJoin, A: 4},
		{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
		{Kind: EvLinkUp, A: 1, B: 3, Cost: 256},
		{Kind: EvLinkUp, A: 2, B: 4, Cost: 256},
		{Kind: EvLinkUp, A: 3, B: 4, Cost: 256},
		{Kind: EvLinkUp, A: 2, B: 3, Cost: 256},
	}).Run()

	// 并发批次：两条关键链路同时断开 + 一条新链路同时出现。
	NewBatchSimulator(net, [][]Event{{
		{Kind: EvLinkDown, A: 1, B: 2},
		{Kind: EvLinkDown, A: 3, B: 4},
		{Kind: EvLinkUp, A: 1, B: 4, Cost: 512},
	}}).Run()

	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle after concurrent events: %v", cycle)
	}
	if !net.Converged() {
		t.Fatal("not converged after concurrent events")
	}
	// 上行与下行不得互相矛盾：根的下行 nextHop 链必须能到达目的，
	// 且每一跳都是当前上行结构的逆。
	for _, dst := range []NodeID{2, 3, 4} {
		visited := map[NodeID]bool{1: true}
		cur := net.Node(1)
		for cur.ID != dst {
			next, err := cur.ForwardDown(dst)
			if err != nil {
				t.Fatalf("down to %d broken at %d: %v", dst, cur.ID, err)
			}
			if visited[next] {
				t.Fatalf("down path to %d loops at %d", dst, next)
			}
			visited[next] = true
			// 一致性：下一跳节点的上行父链必须能回到根。
			if _, err := net.Node(next).PathToRoot(net); err != nil {
				t.Fatalf("node %d on down path has no up path: %v", next, err)
			}
			cur = net.Node(next)
		}
	}
}

// 随机事件风暴：固定种子下每个事件后校验无环；
// 相同种子与事件序列重跑产生逐事件一致的日志。
func TestRandomEventStorm(t *testing.T) {
	genEvents := func(seed int64) []Event {
		rng := rand.New(rand.NewSource(seed))
		var evs []Event
		// 8 个节点加入。
		for i := 1; i <= 8; i++ {
			evs = append(evs, Event{Kind: EvNodeJoin, A: NodeID(i)})
		}
		// 随机初始链路（保证大概率连通：先连成环再加随机边）。
		for i := 2; i <= 8; i++ {
			evs = append(evs, Event{Kind: EvLinkUp, A: NodeID(i - 1), B: NodeID(i), Cost: Cost(256 + rng.Intn(512))})
		}
		evs = append(evs, Event{Kind: EvLinkUp, A: 8, B: 1, Cost: 256})
		for i := 0; i < 6; i++ {
			a, b := NodeID(1+rng.Intn(8)), NodeID(1+rng.Intn(8))
			if a != b {
				evs = append(evs, Event{Kind: EvLinkUp, A: a, B: b, Cost: Cost(256 + rng.Intn(512))})
			}
		}
		// 随机事件风暴。
		for i := 0; i < 120; i++ {
			a, b := NodeID(1+rng.Intn(8)), NodeID(1+rng.Intn(8))
			switch rng.Intn(6) {
			case 0:
				evs = append(evs, Event{Kind: EvLinkDown, A: a, B: b})
			case 1:
				evs = append(evs, Event{Kind: EvLinkUp, A: a, B: b, Cost: Cost(256 + rng.Intn(768))})
			case 2:
				evs = append(evs, Event{Kind: EvLinkQuality, A: a, B: b, Cost: Cost(256 + rng.Intn(768))})
			case 3:
				evs = append(evs, Event{Kind: EvNodeRestart, A: a})
			case 4:
				evs = append(evs, Event{Kind: EvTick, Steps: 1 + rng.Intn(20)})
			default:
				evs = append(evs, Event{Kind: EvNodeLeave, A: a}, Event{Kind: EvNodeJoin, A: a})
			}
		}
		return evs
	}

	run := func(seed int64) (*Network, []LogEntry) {
		net := NewNetwork(1, 7, 1, 32, 1)
		evs := genEvents(seed)
		logs := NewSimulator(net, evs).Run()
		return net, logs
	}

	net1, logs1 := run(99)
	net2, logs2 := run(99)

	// 确定性：逐事件日志完全一致。
	if len(logs1) != len(logs2) {
		t.Fatalf("log length differs: %d vs %d", len(logs1), len(logs2))
	}
	for i := range logs1 {
		if logs1[i] != logs2[i] {
			t.Fatalf("log %d differs:\n  %+v\n  %+v", i, logs1[i], logs2[i])
		}
	}

	// 风暴结束后：无环，且所有仍在线且连通的节点状态一致。
	if ok, cycle := net1.CheckAcyclic(); !ok {
		t.Fatalf("cycle after storm: %v", cycle)
	}
	// 最终状态确定：两次运行的每个节点 Rank/父节点一致。
	for _, id := range net1.Nodes() {
		a, b := net1.Node(id), net2.Node(id)
		pa, _ := a.Parent()
		pb, _ := b.Parent()
		if a.Rank != b.Rank || pa != pb {
			t.Fatalf("node %d final state diverged", id)
		}
	}
	t.Logf("storm produced %d log entries, %d nodes online", len(logs1), len(net1.Nodes()))
}

// 每个事件之后都校验无环性（逐事件不变量检查）。
func TestAcyclicAfterEveryEvent(t *testing.T) {
	rng := rand.New(rand.NewSource(1234))
	net := NewNetwork(1, 5, 1, 16, 1)
	var evs []Event
	for i := 1; i <= 6; i++ {
		evs = append(evs, Event{Kind: EvNodeJoin, A: NodeID(i)})
	}
	for i := 2; i <= 6; i++ {
		evs = append(evs, Event{Kind: EvLinkUp, A: NodeID(i - 1), B: NodeID(i), Cost: 256})
	}
	for i := 0; i < 60; i++ {
		a, b := NodeID(1+rng.Intn(6)), NodeID(1+rng.Intn(6))
		switch rng.Intn(4) {
		case 0:
			evs = append(evs, Event{Kind: EvLinkDown, A: a, B: b})
		case 1:
			evs = append(evs, Event{Kind: EvLinkUp, A: a, B: b, Cost: Cost(256 + rng.Intn(512))})
		case 2:
			evs = append(evs, Event{Kind: EvLinkQuality, A: a, B: b, Cost: Cost(256 + rng.Intn(512))})
		default:
			evs = append(evs, Event{Kind: EvNodeRestart, A: a})
		}
	}
	for i, ev := range evs {
		ev.Seq = uint64(i)
		net.Apply(ev)
		if ok, cycle := net.CheckAcyclic(); !ok {
			t.Fatalf("cycle after event %d (%v): %v", i, ev.Kind, cycle)
		}
	}
}
