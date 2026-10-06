package mesh

import "testing"

// 菱形拓扑中劣化当前父链路，验证父节点切换、无环与下行表收敛。
//
//	1(root) — 2 — 4
//	1(root) — 3 — 4
func buildDiamond(t *testing.T) *Network {
	t.Helper()
	net := NewNetwork(1, 42, 1, 8, 1)
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
	if !net.Converged() {
		t.Fatal("diamond not converged")
	}
	if p, _ := net.Node(4).Parent(); p != 2 {
		t.Fatalf("node4 initial parent = %v, want 2", p)
	}
	return net
}

func TestParentSwitchOnLinkDegradation(t *testing.T) {
	net := buildDiamond(t)

	// 劣化 2-4 链路：经 2 的代价变为 256+768=1024，经 3 仍为 512。
	NewSimulator(net, []Event{
		{Kind: EvLinkQuality, A: 2, B: 4, Cost: 768},
	}).Run()

	if p, _ := net.Node(4).Parent(); p != 3 {
		t.Fatalf("node4 parent after degradation = %v, want 3", p)
	}
	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle after parent switch: %v", cycle)
	}
	// 下行路径必须与新的上行结构一致。
	if e := net.Node(1).Down()[4]; e.nextHop != 3 {
		t.Fatalf("root down nextHop for 4 = %v, want 3", e.nextHop)
	}
	if _, ok := net.Node(2).Down()[4]; ok {
		t.Fatal("node2 still has stale down entry for 4")
	}
}

// 小幅抖动不得触发父节点切换（滞后防抖）。
func TestHysteresisPreventsFlapping(t *testing.T) {
	net := buildDiamond(t)
	// 2-4 轻微劣化：经 2 为 256+272=528，经 3 为 512，差 16 不超滞后。
	NewSimulator(net, []Event{
		{Kind: EvLinkQuality, A: 2, B: 4, Cost: 272},
	}).Run()
	if p, _ := net.Node(4).Parent(); p != 2 {
		t.Fatalf("parent flapped on minor degradation: %v", p)
	}
}

// 链路反复抖动后网络仍收敛且无环。
func TestLinkFlappingConverges(t *testing.T) {
	net := buildDiamond(t)
	var evs []Event
	for i := 0; i < 5; i++ {
		evs = append(evs,
			Event{Kind: EvLinkDown, A: 2, B: 4},
			Event{Kind: EvLinkUp, A: 2, B: 4, Cost: 256},
		)
	}
	NewSimulator(net, evs).Run()

	if !net.Converged() {
		t.Fatal("not converged after flapping")
	}
	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle after flapping: %v", cycle)
	}
	// 每个节点都必须能给出到根的路径。
	for _, id := range net.Nodes() {
		if _, err := net.Node(id).PathToRoot(net); err != nil {
			t.Fatalf("node %d has no path to root: %v", id, err)
		}
	}
}

// 父链路断开且无备用父时，转发必须返回可区分原因而非静默丢弃。
func TestForwardFailureReasons(t *testing.T) {
	net := NewNetwork(1, 42, 1, 8, 1)
	NewSimulator(net, []Event{
		{Kind: EvNodeJoin, A: 1},
		{Kind: EvNodeJoin, A: 2},
		{Kind: EvNodeJoin, A: 3}, // 3 无链路，未接入
		{Kind: EvLinkUp, A: 1, B: 2, Cost: 256},
	}).Run()

	// 未接入节点：stale-state。
	if _, err := net.Node(3).ForwardUp(); err == nil || err.Reason != FwdStaleState {
		t.Fatalf("unjoined node forward = %v, want stale-state", err)
	}
	// 下行不可达：unreachable。
	if _, err := net.Node(1).ForwardDown(3); err == nil || err.Reason != FwdUnreachable {
		t.Fatalf("root down to 3 = %v, want unreachable", err)
	}

	// 断开 1-2，节点 2 失去唯一父节点：no-route。
	NewSimulator(net, []Event{{Kind: EvLinkDown, A: 1, B: 2}}).Run()
	if _, err := net.Node(2).ForwardUp(); err == nil || err.Reason != FwdNoRoute {
		t.Fatalf("orphaned node forward = %v, want no-route", err)
	}
	if net.Converged() {
		t.Fatal("network with orphan should not report converged")
	}
}

// 父链路断开但存在备用父时，本地修复切换到备用父且不丢环。
func TestLocalRepairToBackupParent(t *testing.T) {
	net := buildDiamond(t)
	NewSimulator(net, []Event{{Kind: EvLinkDown, A: 2, B: 4}}).Run()

	if p, _ := net.Node(4).Parent(); p != 3 {
		t.Fatalf("node4 parent after link-down = %v, want 3", p)
	}
	if ok, cycle := net.CheckAcyclic(); !ok {
		t.Fatalf("cycle after local repair: %v", cycle)
	}
	if e := net.Node(1).Down()[4]; e.nextHop != 3 {
		t.Fatalf("root down nextHop for 4 = %v, want 3", e.nextHop)
	}
}
