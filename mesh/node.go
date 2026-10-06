package mesh

import (
	"fmt"
	"sort"
)

// poisonCost 是 DAO 撤销路由时使用的代价。
const poisonCost = LinkQuality(1e15)

// isPoison 报告 DAO 代价是否为撤销通告。
func isPoison(c LinkQuality) bool { return c >= poisonCost }

// maxTTL 是数据报文的最大跳数，耗尽判定为环路。
const maxTTL = 64

// neighbor 记录一个候选父节点（邻居）的状态。
type neighbor struct {
	id      NodeID
	rank    Rank
	version Version
	seq     SeqNo
	link    LinkQuality // 到该邻居的链路质量
}

// cost 返回经由此邻居上行的总代价。
func (n neighbor) cost() LinkQuality { return LinkQuality(float64(n.rank)) + n.link }

// downRoute 是一条下行路由表项。
type downRoute struct {
	dest    NodeID
	nextHop NodeID // 子节点方向下一跳
	cost    LinkQuality
	pathSeq SeqNo // 目的节点通告的路径序列号，大的更新
	seq     SeqNo // 目的节点代次，旧代次的路由一律作废
}

// Node 是自组网中的一个节点。
//
// 无环性由三条规则共同保证：
//   - R1: parent.Rank 严格小于自身 Rank（选父时构造保证，收到父 DIO 时校验）；
//   - R2: 不得选择下行路由表中的子孙作为父节点；
//   - R3: 数据面校验报文 Rank/TTL，发现不一致立即触发本地修复。
type Node struct {
	id      NodeID
	version Version
	seq     SeqNo
	rank    Rank
	isRoot  bool

	parent  NodeID              // 首选父节点，InvalidNodeID 表示无父
	parents map[NodeID]neighbor // 候选父节点集
	down    map[NodeID]downRoute
	pathSeq SeqNo // 自身下行路径序列号，换父时递增

	everAttached bool // 曾经接入过（区分"切换中"与"从未接入"）

	trickle *Trickle
	net     *Network
}

func newNode(net *Network, id NodeID, isRoot bool, seq SeqNo, version Version) *Node {
	n := &Node{
		id:      id,
		seq:     seq,
		isRoot:  isRoot,
		parent:  InvalidNodeID,
		parents: make(map[NodeID]neighbor),
		down:    make(map[NodeID]downRoute),
		net:     net,
		trickle: NewTrickle(net.cfg.TrickleImin, net.cfg.TrickleImax, net.cfg.TrickleK, net.rng),
	}
	if isRoot {
		n.rank = RootRank
		n.version = version
		n.everAttached = true
	} else {
		n.rank = InfiniteRank
	}
	return n
}

// ID 返回节点标识。
func (n *Node) ID() NodeID { return n.id }

// Rank 返回节点当前 Rank。
func (n *Node) Rank() Rank { return n.rank }

// Parent 返回当前首选父节点。
func (n *Node) Parent() NodeID { return n.parent }

// Attached 报告节点是否已接入拓扑（有到根的可用路径）。
func (n *Node) Attached() bool { return n.isRoot || n.parent != InvalidNodeID }

// UpwardPath 给出本节点到达根节点的一条可用路径及其代价。
// 沿父链遍历并做 visited 检测；若父链断裂或成环则返回无效路径。
func (n *Node) UpwardPath() Path {
	if !n.Attached() {
		return Path{}
	}
	p := Path{Hops: []NodeID{n.id}}
	visited := map[NodeID]bool{n.id: true}
	cur := n
	for !cur.isRoot {
		next := cur.parent
		if next == InvalidNodeID {
			return Path{}
		}
		if visited[next] {
			return Path{} // 父链成环，路径不可用
		}
		visited[next] = true
		p.Cost += n.net.Link(cur.id, next)
		p.Hops = append(p.Hops, next)
		cur = n.net.Get(next)
		if cur == nil {
			return Path{}
		}
	}
	return p
}

// DownwardNextHop 给出到达 dst 的下行下一跳；ok=false 表示无下行路由。
func (n *Node) DownwardNextHop(dst NodeID) (next NodeID, ok bool) {
	if dst == n.id {
		return n.id, true
	}
	rt, ok := n.down[dst]
	if !ok {
		return InvalidNodeID, false
	}
	return rt.nextHop, true
}

// ---------- 选父 ----------

// bestCandidate 在候选父集中选出代价最小者。
// 排除：链路不可用、Rank 为无穷、自身子孙（R2）。
// 平局按 NodeID 小者优先，保证确定性。
func (n *Node) bestCandidate() (neighbor, bool) {
	cands := make([]neighbor, 0, len(n.parents))
	for _, nb := range n.parents {
		if nb.link <= LinkDown || nb.link > MaxUsableLink {
			continue
		}
		if nb.rank >= InfiniteRank {
			continue
		}
		if _, isDesc := n.down[nb.id]; isDesc {
			continue // R2：子孙不得为父
		}
		cands = append(cands, nb)
	}
	if len(cands) == 0 {
		return neighbor{}, false
	}
	sort.Slice(cands, func(i, j int) bool {
		ci, cj := cands[i].cost(), cands[j].cost()
		if ci != cj {
			return ci < cj
		}
		return cands[i].id < cands[j].id
	})
	return cands[0], true
}

// selectParent 重新计算首选父节点；父或 Rank 变化时触发传播动作。
func (n *Node) selectParent() {
	if n.isRoot {
		return
	}
	oldParent, oldRank := n.parent, n.rank
	best, ok := n.bestCandidate()
	if !ok {
		n.parent = InvalidNodeID
		n.rank = InfiniteRank
	} else {
		n.parent = best.id
		n.rank = best.rank + 1 // R1：构造保证 parent.Rank < self.Rank
		n.version = best.version
	}
	if n.parent != oldParent {
		n.onParentChange(oldParent, n.parent)
		// 立即广播新状态：父切换或脱离（rank=Inf，poison 语义）
		// 必须及时通知子树，否则子节点会沿失效父链空转。
		n.broadcastDIO()
	} else if n.rank != oldRank {
		n.trickle.Reset()
		n.broadcastDIO() // Rank 变化需及时传播给子树
		n.net.logf(n.id, "rank-change", fmt.Sprintf("rank=%d parent=%d", n.rank, n.parent))
	}
}

// onParentChange 在父节点切换时执行：毒化旧路径、登记新路径、加速控制传播。
// 切换过程本身不产生环：新父 Rank 严格小于自身新 Rank（R1），
// 旧父方向立即收到 poison DAO，撤销其指向本节点的下行路由。
func (n *Node) onParentChange(old, new NodeID) {
	n.everAttached = n.everAttached || new != InvalidNodeID
	n.trickle.Reset()
	if old != InvalidNodeID && old != new {
		n.sendDAOpayload(old, n.id, n.seq, n.pathSeq, poisonCost)
		for _, rt := range n.down {
			n.sendDAOpayload(old, rt.dest, rt.seq, rt.pathSeq, poisonCost)
		}
	}
	if new != InvalidNodeID {
		n.pathSeq++
		n.sendDAOpayload(new, n.id, n.seq, n.pathSeq, 0)
		for _, rt := range n.down {
			n.sendDAOpayload(new, rt.dest, rt.seq, rt.pathSeq, rt.cost)
		}
	}
	n.net.logf(n.id, "select-parent", fmt.Sprintf("old=%d new=%d rank=%d", old, new, n.rank))
}

// ---------- DIO（上行结构维护） ----------

// broadcastDIO 向所有邻居发送拓扑信息。
func (n *Node) broadcastDIO() {
	for _, nb := range n.net.sortedNeighbors(n.id) {
		n.net.deliverDIO(Msg{
			Type: MsgDIO, From: n.id, To: nb,
			Version: n.version, Seq: n.seq, Rank: n.rank,
		})
	}
}

// OnDIO 处理拓扑信息广播。
func (n *Node) OnDIO(m Msg) {
	if n.isRoot {
		return // 根不接受任何父
	}
	// 陈旧版本：来自旧拓扑的消息一律忽略。
	if n.version != 0 && m.Version < n.version {
		n.net.logf(n.id, "drop-stale-dio", fmt.Sprintf("from=%d ver=%d my-ver=%d", m.From, m.Version, n.version))
		return
	}
	// 拓扑重建：更高版本出现，旧状态全部作废。
	if m.Version > n.version && n.version != 0 {
		n.parents = make(map[NodeID]neighbor)
		n.down = make(map[NodeID]downRoute)
		n.parent = InvalidNodeID
		n.rank = InfiniteRank
		n.trickle.Reset()
		n.net.logf(n.id, "version-reset", fmt.Sprintf("ver=%d->%d", n.version, m.Version))
	}
	// 旧化身：同一 NodeID 的旧代次消息不得覆盖新状态（结果确定：代次大者胜）。
	if nb, ok := n.parents[m.From]; ok && m.Seq < nb.seq {
		n.net.logf(n.id, "drop-stale-dio", fmt.Sprintf("from=%d seq=%d known-seq=%d", m.From, m.Seq, nb.seq))
		return
	}
	link := n.net.Link(m.From, n.id)
	if link <= LinkDown {
		return
	}
	// 一致性判断：无新信息则计入抑制计数，有新信息则重置窗口。
	if nb, ok := n.parents[m.From]; ok &&
		nb.rank == m.Rank && nb.version == m.Version && nb.seq == m.Seq {
		n.trickle.Consistent()
	} else {
		n.trickle.Reset()
	}
	n.parents[m.From] = neighbor{id: m.From, rank: m.Rank, version: m.Version, seq: m.Seq, link: link}
	// R1 校验：当前父的 Rank 不再严格小于自身时必须重选。
	if n.parent != InvalidNodeID {
		if p, ok := n.parents[n.parent]; ok && p.rank+1 > n.rank {
			n.net.logf(n.id, "rank-violation", fmt.Sprintf("parent=%d p-rank=%d my-rank=%d", n.parent, p.rank, n.rank))
		}
	}
	n.selectParent()
}

// ---------- DAO（下行路由维护） ----------

// sendDAOpayload 向 to 发送一条关于 dest 的 DAO。
func (n *Node) sendDAOpayload(to NodeID, dest NodeID, destSeq SeqNo, pathSeq SeqNo, cost LinkQuality) {
	m := Msg{
		Type: MsgDAO, From: n.id, To: to,
		Version: n.version, Seq: n.seq,
		AdvDest: dest, AdvSeq: destSeq, AdvVia: n.id,
		PathSeq: pathSeq, AdvCost: cost,
	}
	n.net.deliverDAO(m)
}

// OnDAO 处理下行路由通告：更新本地下行表并沿父链向上传播。
func (n *Node) OnDAO(m Msg) {
	if m.AdvDest == n.id {
		return // 不为自己建路由
	}
	rt, known := n.down[m.AdvDest]
	// 陈旧判定：代次更旧，或同代次但路径序列号更旧。
	if known && (m.AdvSeq < rt.seq || (m.AdvSeq == rt.seq && m.PathSeq < rt.pathSeq)) {
		n.net.logf(n.id, "drop-stale-dao", fmt.Sprintf("dest=%d from=%d", m.AdvDest, m.From))
		return
	}
	if isPoison(m.AdvCost) {
		// 撤销：仅当该路由确实经由通告者时删除，并向上传播。
		if known && rt.nextHop == m.From {
			delete(n.down, m.AdvDest)
			n.net.logf(n.id, "dao-poison", fmt.Sprintf("dest=%d via=%d", m.AdvDest, m.From))
			if !n.isRoot && n.parent != InvalidNodeID {
				n.sendDAOpayload(n.parent, m.AdvDest, rt.seq, rt.pathSeq, poisonCost)
			}
		}
		n.sendDAOAck(m)
		return
	}
	cost := m.AdvCost + n.net.Link(m.From, n.id)
	n.down[m.AdvDest] = downRoute{
		dest: m.AdvDest, nextHop: m.From, cost: cost,
		pathSeq: m.PathSeq, seq: m.AdvSeq,
	}
	n.net.logf(n.id, "dao", fmt.Sprintf("dest=%d via=%d cost=%.1f pseq=%d", m.AdvDest, m.From, cost, m.PathSeq))
	// 沿父链继续传播，直到根。
	if !n.isRoot && n.parent != InvalidNodeID {
		n.sendDAOpayload(n.parent, m.AdvDest, m.AdvSeq, m.PathSeq, cost)
	}
	n.sendDAOAck(m)
}

// sendDAOAck 确认收到 DAO。
func (n *Node) sendDAOAck(dao Msg) {
	n.net.deliverDAOAck(Msg{Type: MsgDAOAck, From: n.id, To: dao.From, Version: n.version, Seq: n.seq})
}

// OnDAOAck 处理 DAO 确认。当前实现中切换在选父时同步完成，
// ACK 仅作为协议完整性保留，收到后无需额外动作。
func (n *Node) OnDAOAck(m Msg) {}

// ---------- 数据面 ----------

// SendUp 由源节点注入一条上行报文，返回最终转发结果。
func (n *Node) SendUp(payloadID uint64) ForwardErr {
	m := Msg{
		Type: MsgDataUp, From: n.id, Src: n.id, Dst: n.net.root,
		PayloadID: payloadID, Rank: n.rank, Version: n.version, Seq: n.seq,
		TTL: maxTTL,
	}
	return n.OnDataUp(m)
}

// OnDataUp 处理（或转发）一条上行报文。
func (n *Node) OnDataUp(m Msg) ForwardErr {
	if n.isRoot {
		n.net.logf(n.id, "deliver-up", fmt.Sprintf("src=%d id=%d", m.Src, m.PayloadID))
		return ForwardOK
	}
	// TTL 耗尽是环的强信号，优先于本地状态判定。
	if m.TTL <= 0 {
		n.net.logf(n.id, "loop-detected", fmt.Sprintf("ttl-exhausted src=%d", m.Src))
		n.repairLocal()
		n.nack(m, ErrLoopDetected)
		return ErrLoopDetected
	}
	if n.parent == InvalidNodeID {
		cause := ErrNoUpwardRoute
		if n.everAttached && len(n.parents) > 0 {
			cause = ErrParentSwitching // 有候选但暂未收敛，属暂时性故障
		}
		n.nack(m, cause)
		return cause
	}
	// R3：上行报文必须来自 Rank 严格更大的节点，否则判定为环。
	if m.From != n.id && m.Rank <= n.rank {
		n.net.logf(n.id, "loop-detected", fmt.Sprintf("from=%d m-rank=%d my-rank=%d", m.From, m.Rank, n.rank))
		n.repairLocal()
		n.nack(m, ErrLoopDetected)
		return ErrLoopDetected
	}
	m.From = n.id
	m.Rank = n.rank
	m.TTL--
	n.net.logf(n.id, "fwd-up", fmt.Sprintf("src=%d id=%d via=%d", m.Src, m.PayloadID, n.parent))
	parent := n.net.Get(n.parent)
	if parent == nil {
		n.nack(m, ErrNoUpwardRoute)
		return ErrNoUpwardRoute
	}
	return parent.OnDataUp(m)
}

// SendDown 由根节点注入一条下行报文，返回最终转发结果。
func (n *Node) SendDown(dst NodeID, payloadID uint64) ForwardErr {
	m := Msg{
		Type: MsgDataDown, From: n.id, Src: n.id, Dst: dst,
		PayloadID: payloadID, Rank: n.rank, Version: n.version, Seq: n.seq,
		TTL: maxTTL,
	}
	return n.OnDataDown(m)
}

// OnDataDown 处理（或转发）一条下行报文。
func (n *Node) OnDataDown(m Msg) ForwardErr {
	if m.Dst == n.id {
		n.net.logf(n.id, "deliver-down", fmt.Sprintf("id=%d", m.PayloadID))
		return ForwardOK
	}
	if m.TTL <= 0 {
		n.net.logf(n.id, "loop-detected", fmt.Sprintf("ttl-exhausted dst=%d", m.Dst))
		n.repairLocal()
		n.nack(m, ErrLoopDetected)
		return ErrLoopDetected
	}
	rt, ok := n.down[m.Dst]
	if !ok {
		cause := ErrNoDownwardRoute
		if n.net.Get(m.Dst) == nil {
			cause = ErrDestinationDown
		}
		n.nack(m, cause)
		return cause
	}
	next := n.net.Get(rt.nextHop)
	if next == nil {
		delete(n.down, m.Dst)
		n.nack(m, ErrNoDownwardRoute)
		return ErrNoDownwardRoute
	}
	m.From = n.id
	m.Rank = n.rank
	m.TTL--
	n.net.logf(n.id, "fwd-down", fmt.Sprintf("dst=%d id=%d via=%d", m.Dst, m.PayloadID, rt.nextHop))
	return next.OnDataDown(m)
}

// nack 在转发失败时向源节点回送否定确认（带内通知），并记录日志。
func (n *Node) nack(m Msg, cause ForwardErr) {
	n.net.logf(n.id, "nack", fmt.Sprintf("src=%d dst=%d id=%d cause=%s", m.Src, m.Dst, m.PayloadID, cause))
	if src := n.net.Get(m.Src); src != nil && m.Src != n.id {
		src.OnNACK(Msg{
			Type: MsgNACK, From: n.id, To: m.Src,
			PayloadID: m.PayloadID, NACKCause: cause,
		})
	}
}

// OnNACK 记录一次否定确认。
func (n *Node) OnNACK(m Msg) {
	n.net.logf(n.id, "recv-nack", fmt.Sprintf("from=%d id=%d cause=%s", m.From, m.PayloadID, m.NACKCause))
}

// ---------- 链路变化与修复 ----------

// OnLinkChange 处理到 peer 的链路质量变化；q <= 0 表示断开。
func (n *Node) OnLinkChange(peer NodeID, q LinkQuality) {
	if q <= LinkDown || q > MaxUsableLink {
		delete(n.parents, peer)
		// 清理经由该邻居的下行路由，并向上毒化。
		for dest, rt := range n.down {
			if rt.nextHop == peer {
				delete(n.down, dest)
				if !n.isRoot && n.parent != InvalidNodeID {
					n.sendDAOpayload(n.parent, dest, rt.seq, rt.pathSeq, poisonCost)
				}
			}
		}
	} else if nb, ok := n.parents[peer]; ok {
		nb.link = q
		n.parents[peer] = nb
	}
	n.trickle.Reset() // 链路变化是拓扑事件，需及时传播
	n.selectParent()
}

// repairLocal 在检测到环路或不一致时执行本地修复：
// 丢弃可疑父节点状态，重置 Rank 并重新选父。
func (n *Node) repairLocal() {
	if n.parent != InvalidNodeID {
		delete(n.parents, n.parent)
		n.parent = InvalidNodeID
	}
	n.rank = InfiniteRank
	n.trickle.Reset()
	n.net.logf(n.id, "repair", "local-repair")
	n.selectParent()
}

// tick 推进节点的 Trickle 定时器；到达发送窗口且已接入时广播 DIO。
func (n *Node) tick() {
	if n.trickle.Tick() && n.Attached() {
		n.broadcastDIO()
	}
}
