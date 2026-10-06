package mesh

import "math/rand"

// parentHysteresis 是父节点切换滞后量：新候选的代价必须比当前父
// 至少低该值才触发切换，避免链路质量小幅抖动引起父节点来回震荡。
const parentHysteresis = Cost(16)

// MaxRank 是 Rank 上限。互为孤岛的节点互相选父会导致 Rank 交替爬升
//
//	（count-to-infinity）；超过 MaxRank 的邻居不得再被选为父，
//
// 使 Rank 爬升有界且快速触顶，保证传播在有限步内终止。
// 取值 65536 相当于 256 跳完美链路，对本地自组网规模足够。
const MaxRank = Cost(65536)

// addCost 做防溢出的代价加法，任何一侧为 InfiniteCost 或结果溢出
// 都视为不可达。
func addCost(a, b Cost) Cost {
	if a >= InfiniteCost || b >= InfiniteCost {
		return InfiniteCost
	}
	s := a + b
	if s < a {
		return InfiniteCost
	}
	return s
}

// Neighbor 是邻居表项，记录到某邻居的链路代价与其通告的路由信息。
type Neighbor struct {
	ID       NodeID // 邻居节点
	LinkCost Cost   // 本节点到邻居的链路代价
	Rank     Cost   // 邻居通告的到根代价
	DTSN     uint32 // 邻居经 DIO 通告的下行触发序列号
	Version  uint32 // 邻居所属 DODAG 版本
	Seq      uint32 // 邻居的化身序列号（重启时递增）
	Seen     uint64 // 最近一次收到该邻居通告的时钟
	daoSeq   uint32 // 已接收的邻居 DAO 序列号（DAO 去重专用，与 DTSN 独立）
}

// downEntry 是下行路由表项。
type downEntry struct {
	nextHop NodeID // 下一跳（某个子节点）
	cost    Cost   // 经该子节点到目的的代价
	seq     uint32 // 表项来源子节点的 DTSN，用于去陈旧
}

// daoMessage 是子节点发给父节点的下行路由通告。
type daoMessage struct {
	from    NodeID          // 发送者（直接子节点）
	fromSeq uint32          // 发送者的化身序列号
	dtsn    uint32          // 发送者的下行序列号
	noPath  bool            // true 表示 no-path：请求清除经发送者学到的全部表项
	entries map[NodeID]Cost // dest -> 发送者到 dest 的代价（含发送者自身，代价 0）
}

// Node 是网络中的一个节点，维护上行父节点、下行路由表与 DODAG 状态。
//
// 无环不变量：任意时刻，沿父指针方向 Rank 严格递减；节点绝不选择
// 自身下行表中的后代作为父节点。两条规则共同保证任意时刻（包括
// 父节点切换过程中）上行结构无环。
type Node struct {
	ID NodeID

	Version uint32 // 所属 DODAG 版本（根重启时递增）
	Seq     uint32 // 节点自身化身序列号（重启时递增）
	Rank    Cost   // 本节点到根的代价（根为 0）

	parent NodeID // 当前首选父节点
	hasPar bool   // 是否已有父节点
	joined bool   // 是否已接入 DODAG

	neighbors map[NodeID]*Neighbor // 邻居表
	down      map[NodeID]downEntry // 下行路由表：目的 -> 下一跳
	dtsn      uint32               // 下行触发序列号

	trickle *Trickle // 控制消息抑制器
}

// NewNode 创建一个节点实例。rng 为节点级确定性随机源。
func NewNode(id NodeID, imin, imax uint64, k int, rng *rand.Rand) *Node {
	return &Node{
		ID:        id,
		Rank:      InfiniteCost,
		neighbors: make(map[NodeID]*Neighbor),
		down:      make(map[NodeID]downEntry),
		dtsn:      1, // 从 1 开始，使首条 DAO 不被零值去重误拒
		trickle:   NewTrickle(imin, imax, k, rng),
	}
}

// IsRoot 报告本节点是否为根（已接入且 Rank 为 0）。
func (n *Node) IsRoot() bool { return n.joined && n.Rank == RootCost }

// Joined 报告节点是否已接入 DODAG。
func (n *Node) Joined() bool { return n.joined }

// Parent 返回当前父节点与是否存在。
func (n *Node) Parent() (NodeID, bool) { return n.parent, n.hasPar }

// DTSN 返回当前下行触发序列号。
func (n *Node) DTSN() uint32 { return n.dtsn }

// Down 返回下行路由表的只读快照（供网络层与测试使用）。
func (n *Node) Down() map[NodeID]downEntry { return n.down }

// NeighborOf 返回指定邻居的表项。
func (n *Node) NeighborOf(id NodeID) *Neighbor { return n.neighbors[id] }

// joinAsRoot 将节点初始化为 DODAG 根。
func (n *Node) joinAsRoot(version uint32) {
	n.Version = version
	n.Rank = RootCost
	n.joined = true
	n.hasPar = false
	n.trickle.Reset()
}

// AddLink 在本地邻居表中建立链路（仅记录链路代价，路由信息待 DIO 填充）。
func (n *Node) AddLink(peer NodeID, cost Cost) {
	if nb, ok := n.neighbors[peer]; ok {
		nb.LinkCost = cost
	} else {
		n.neighbors[peer] = &Neighbor{ID: peer, LinkCost: cost, Rank: InfiniteCost}
	}
}

// UpdateLink 更新到邻居的链路代价；链路代价变化立即触发本地重选。
// 返回本地路由状态是否变化。
func (n *Node) UpdateLink(peer NodeID, cost Cost) bool {
	if nb, ok := n.neighbors[peer]; ok {
		nb.LinkCost = cost
	}
	return n.reelect()
}

// RemoveLink 删除到邻居的链路，清除经该邻居学到的下行表项，
// 并立即本地重选（父链路可能已断）。链路状态是本地资源，
// 其变化不应等待邻居通告才生效。
// 返回本地路由状态（含下行表）是否变化。
func (n *Node) RemoveLink(peer NodeID) bool {
	delete(n.neighbors, peer)
	downChanged := false
	for dst, e := range n.down {
		if e.nextHop == peer {
			delete(n.down, dst)
			downChanged = true
		}
	}
	if downChanged {
		n.dtsn++
	}
	return n.reelect() || downChanged
}

// reelect 重新选择父节点并重算 Rank。返回父节点或 Rank 是否变化。
//
// 候选父必须满足：
//  1. 链路存在且邻居已通告有效 Rank（已接入同一或更新版本）；
//  2. 候选不在本节点下行表中（不是后代）——防环规则一；
//  3. 若当前父仍可用，新候选须优于当前父至少 parentHysteresis —— 滞后防抖。
//
// 根节点（Rank==0）永不选父。
func (n *Node) reelect() bool {
	if n.IsRoot() {
		return false
	}
	oldParent, oldHas, oldRank := n.parent, n.hasPar, n.Rank

	var best NodeID
	bestCost := InfiniteCost
	found := false
	for id, nb := range n.neighbors {
		if nb.Rank >= MaxRank || nb.Version != n.Version {
			continue // 不可达、已饱和或版本不符
		}
		if _, isDesc := n.down[id]; isDesc {
			continue // 后代不得为父
		}
		cand := addCost(nb.Rank, nb.LinkCost)
		if cand >= MaxRank {
			continue // 结果 Rank 饱和，防止 count-to-infinity
		}
		if cand < bestCost || (cand == bestCost && found && id < best) {
			best, bestCost, found = id, cand, true
		}
	}

	// 滞后：当前父仍可用且差距不足时保留，避免抖动。
	if oldHas {
		if nb, ok := n.neighbors[oldParent]; ok && nb.Rank < MaxRank && nb.Version == n.Version {
			if _, isDesc := n.down[oldParent]; !isDesc {
				cur := addCost(nb.Rank, nb.LinkCost)
				if cur < MaxRank && cur <= addCost(bestCost, parentHysteresis) {
					best, bestCost, found = oldParent, cur, true
				}
			}
		}
	}

	if !found {
		n.hasPar = false
		if !n.IsRoot() {
			n.Rank = InfiniteCost
		}
	} else {
		n.hasPar = true
		n.parent = best
		n.Rank = bestCost
	}
	return n.parent != oldParent || n.hasPar != oldHas || n.Rank != oldRank
}

// ReceiveDIO 处理来自邻居的上行结构通告。now 为当前时钟。
// 仅当本节点路由状态（Rank/父节点/下行表/版本）真正变化时返回 true，
// 邻居信息更新但本地决策不变时不触发再传播，保证传播有限终止。
func (n *Node) ReceiveDIO(from NodeID, version, seq uint32, rank Cost, dtsn uint32, now uint64) bool {
	nb, ok := n.neighbors[from]
	if !ok {
		return false // 链路不存在，忽略
	}
	// 陈旧化身：邻居重启前的通告，拒绝混用。
	if seq < nb.Seq {
		return false
	}
	// 陈旧 DODAG 版本：拒绝与当前拓扑混用。
	if n.joined && version < n.Version {
		return false
	}

	changed := false
	// 邻居以新化身出现：其旧化身学到的下行表项全部作废，
	// 并重置 DAO 去重序列（新化身的 DTSN 重新计数）。
	if seq > nb.Seq {
		for dst, e := range n.down {
			if e.nextHop == from {
				delete(n.down, dst)
			}
		}
		nb.daoSeq = 0
		changed = true
	}
	// 迁移到更新的 DODAG 版本：清空下行状态后重新加入。
	if !n.joined || version > n.Version {
		n.Version = version
		n.down = make(map[NodeID]downEntry)
		n.hasPar = false
		n.Rank = InfiniteCost
		n.joined = true
		changed = true
	}

	infoChanged := nb.Rank != rank || nb.DTSN != dtsn || nb.Seq != seq || nb.Version != version
	nb.Rank, nb.DTSN, nb.Seq, nb.Version = rank, dtsn, seq, version
	nb.Seen = now
	if infoChanged {
		n.trickle.Inconsistent()
	} else {
		n.trickle.Consistent()
	}

	if n.reelect() {
		changed = true
	}
	if changed {
		n.trickle.Reset()
	}
	return changed
}

// ReceiveDAO 处理来自子节点的下行路由通告。
// 仅当本节点路由状态真正变化时返回 true。下行表更新后重新选父：
// 若当前父节点出现在下行表中（互选成环的窗口期），立即放弃该父，
// 这是打破 count-to-infinity 环的关键步骤。
func (n *Node) ReceiveDAO(msg daoMessage) bool {
	nb, ok := n.neighbors[msg.from]
	if !ok {
		return false // 链路不存在
	}
	// 陈旧化身的通告直接拒绝。
	if msg.fromSeq < nb.Seq {
		return false
	}
	// no-path 通告：清除经该邻居学到的全部表项。清除是幂等的，
	// 故免 dtsn 去重，保证父节点切换后旧父一定能收到清理通知。
	if msg.noPath {
		changed := false
		for dst, e := range n.down {
			if e.nextHop == msg.from {
				delete(n.down, dst)
				changed = true
			}
		}
		if msg.dtsn > nb.daoSeq {
			nb.daoSeq = msg.dtsn
		}
		if !changed {
			return false
		}
		n.dtsn++
		routeChanged := n.reelect()
		n.trickle.Reset()
		return changed || routeChanged
	}
	// 常规 DAO 去重使用独立的 daoSeq，与 DIO 通告的 DTSN 互不干扰。
	if msg.fromSeq == nb.Seq && msg.dtsn <= nb.daoSeq {
		return false
	}
	nb.Seq, nb.daoSeq = msg.fromSeq, msg.dtsn

	// 全量替换经该子节点学到的表项，并精确比较是否真正变化；
	// 无变化的重复通告不推进 DTSN、不再传播，保证传播有限终止。
	old := make(map[NodeID]Cost)
	for dst, e := range n.down {
		if e.nextHop == msg.from {
			old[dst] = e.cost
			delete(n.down, dst)
		}
	}
	changed := false
	for dst, c := range msg.entries {
		if dst == n.ID {
			continue // 不安装指向自己的表项
		}
		nc := addCost(c, nb.LinkCost)
		if oc, ok := old[dst]; !ok || oc != nc {
			changed = true
		}
		n.down[dst] = downEntry{nextHop: msg.from, cost: nc, seq: msg.dtsn}
		delete(old, dst)
	}
	if len(old) > 0 {
		changed = true // 有表项随通告消失
	}
	if !changed {
		if n.reelect() {
			return true // 父节点可能已成后代，需重新传播
		}
		return false
	}
	n.dtsn++
	routeChanged := n.reelect()
	n.trickle.Reset()
	return changed || routeChanged
}

// DAO 生成发往当前父节点的下行通告：自身（代价 0）加上全部下行表项。
func (n *Node) DAO() daoMessage {
	entries := make(map[NodeID]Cost, len(n.down)+1)
	entries[n.ID] = 0
	for dst, e := range n.down {
		entries[dst] = e.cost
	}
	return daoMessage{from: n.ID, fromSeq: n.Seq, dtsn: n.dtsn, entries: entries}
}

// NoPathDAO 生成发往旧父节点的清理通告，请求其清除经本节点学到的表项。
func (n *Node) NoPathDAO() daoMessage {
	return daoMessage{from: n.ID, fromSeq: n.Seq, dtsn: n.dtsn, noPath: true}
}

// ForwardUp 返回上行下一跳；失败时返回可区分原因。
// 转发前实时校验无环不变量，不变量被破坏时拒绝转发而非静默丢弃。
func (n *Node) ForwardUp() (NodeID, *ForwardError) {
	if !n.joined {
		return 0, &ForwardError{FwdStaleState, "node not joined (e.g. after restart)"}
	}
	if n.IsRoot() {
		return n.ID, nil // 已到根
	}
	if !n.hasPar {
		return 0, &ForwardError{FwdNoRoute, "no parent selected"}
	}
	nb, ok := n.neighbors[n.parent]
	if !ok {
		return 0, &ForwardError{FwdParentMissing, "parent link lost, repair pending"}
	}
	if nb.Rank >= n.Rank {
		return 0, &ForwardError{FwdLoopDetected, "parent rank not strictly lower"}
	}
	return n.parent, nil
}

// ForwardDown 返回到达 dst 的下行下一跳；失败时返回可区分原因。
func (n *Node) ForwardDown(dst NodeID) (NodeID, *ForwardError) {
	if !n.joined {
		return 0, &ForwardError{FwdStaleState, "node not joined"}
	}
	if dst == n.ID {
		return n.ID, nil
	}
	if e, ok := n.down[dst]; ok {
		return e.nextHop, nil
	}
	return 0, &ForwardError{FwdUnreachable, "no downward entry for destination"}
}

// PathToRoot 沿父指针给出到达根节点的一条可用路径及代价。
// 带防御性环检测：理论上不变量保证无环，若检测到环则明确报错。
func (n *Node) PathToRoot(net *Network) (PathInfo, *ForwardError) {
	if !n.joined {
		return PathInfo{}, &ForwardError{FwdStaleState, "node not joined"}
	}
	if n.IsRoot() {
		return PathInfo{NextHop: n.ID, Cost: RootCost, Hops: 0}, nil
	}
	first, ferr := n.ForwardUp()
	if ferr != nil {
		return PathInfo{}, ferr
	}
	visited := map[NodeID]bool{n.ID: true}
	cur, hops, total := n, 0, Cost(0)
	for !cur.IsRoot() {
		next, err := cur.ForwardUp()
		if err != nil {
			return PathInfo{}, err
		}
		if visited[next] {
			return PathInfo{}, &ForwardError{FwdLoopDetected, "cycle on parent chain"}
		}
		visited[next] = true
		nb := cur.neighbors[cur.parent]
		total = addCost(total, nb.LinkCost)
		hops++
		nextNode := net.Node(next)
		if nextNode == nil {
			return PathInfo{}, &ForwardError{FwdParentMissing, "parent not in network"}
		}
		cur = nextNode
	}
	return PathInfo{NextHop: first, Cost: total, Hops: hops}, nil
}

// Restart 清空易失路由状态，以新化身序列号重新加入。
// 旧化身的通告将被邻居依据序列号拒绝，保证陈旧状态不混入新拓扑。
func (n *Node) Restart() {
	n.Seq++
	n.Rank = InfiniteCost
	n.hasPar = false
	n.joined = false
	n.down = make(map[NodeID]downEntry)
	n.neighbors = make(map[NodeID]*Neighbor)
	n.dtsn = 1 // 与 NewNode 保持一致的起始序列号
	n.trickle.Reset()
}

// Tick 推进本地时钟，返回是否需要发送控制消息。
func (n *Node) Tick(steps uint64) bool {
	return n.trickle.Advance(steps)
}
