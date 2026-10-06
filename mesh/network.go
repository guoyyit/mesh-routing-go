package mesh

import (
	"fmt"
	"math/rand"
	"sort"
)

// maxPropagateIter 是单次事件后传播轮数的安全上限；
// 正常拓扑下远未到达即收敛，超过则说明存在设计缺陷，直接 panic 暴露。
const maxPropagateIter = 100000

// Network 是节点与链路的集合，负责施加事件、同步传播控制消息并维护
// 仿真时钟。Apply 是串行的：一批事件施加完毕后统一传播至收敛，
// 因此并发产生的拓扑事件不会破坏无环性，结果与事件顺序唯一对应。
type Network struct {
	root  NodeID
	nodes map[NodeID]*Node
	links map[NodeID]map[NodeID]Cost // 无向链路代价
	rng   *rand.Rand
	seed  int64
	time  uint64

	imin, imax uint64
	k          int

	dioQ  []NodeID        // 待广播 DIO 的节点队列
	daoQ  []NodeID        // 待向上游发 DAO 的节点队列
	inDIO map[NodeID]bool // 队列去重
	inDAO map[NodeID]bool

	dioSent uint64 // 累计 DIO 广播次数（控制开销度量）
	logs    []LogEntry
}

// NewNetwork 创建以 root 为汇聚根的网络。seed 固定随机种子，
// 相同种子与相同事件序列产生逐事件一致的输出。
func NewNetwork(root NodeID, seed int64, imin, imax uint64, k int) *Network {
	return &Network{
		root:  root,
		nodes: make(map[NodeID]*Node),
		links: make(map[NodeID]map[NodeID]Cost),
		rng:   rand.New(rand.NewSource(seed)),
		seed:  seed,
		imin:  imin,
		imax:  imax,
		k:     k,
		inDIO: make(map[NodeID]bool),
		inDAO: make(map[NodeID]bool),
	}
}

// Root 返回根节点 ID。
func (n *Network) Root() NodeID { return n.root }

// Time 返回当前仿真时钟。
func (n *Network) Time() uint64 { return n.time }

// Node 返回指定节点，不存在时返回 nil。
func (n *Network) Node(id NodeID) *Node { return n.nodes[id] }

// Nodes 返回按 ID 排序的全部节点 ID（确定性遍历）。
func (n *Network) Nodes() []NodeID {
	ids := make([]NodeID, 0, len(n.nodes))
	for id := range n.nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Logs 返回迄今为止的逐事件日志。
func (n *Network) Logs() []LogEntry { return n.logs }

// DIOSent 返回累计 DIO 广播次数，用于度量控制消息开销。
func (n *Network) DIOSent() uint64 { return n.dioSent }

func (n *Network) log(node NodeID, format string, args ...interface{}) {
	n.logs = append(n.logs, LogEntry{
		Time:    n.time,
		Node:    node,
		Message: fmt.Sprintf(format, args...),
	})
}

// sortedNeighbors 返回按 ID 排序的邻居列表。
func (n *Network) sortedNeighbors(id NodeID) []NodeID {
	peers := make([]NodeID, 0, len(n.links[id]))
	for p := range n.links[id] {
		peers = append(peers, p)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i] < peers[j] })
	return peers
}

func (n *Network) enqueueDIO(id NodeID) {
	if !n.inDIO[id] {
		n.inDIO[id] = true
		n.dioQ = append(n.dioQ, id)
	}
}

func (n *Network) enqueueDAO(id NodeID) {
	if !n.inDAO[id] {
		n.inDAO[id] = true
		n.daoQ = append(n.daoQ, id)
	}
}

// propagate 同步传播控制消息直到收敛。DIO（上行结构）与 DAO（下行表）
// 严格交替处理：若 DIO 绝对优先，rank 爬升会持续产生 DIO 使 DAO 饿死，
// 下行表得不到更新，互选环无法被打破。
// 队列按 FIFO 且邻居按 ID 排序投递，传播结果确定。
func (n *Network) propagate() {
	for i := 0; i < maxPropagateIter; i++ {
		if len(n.dioQ) == 0 && len(n.daoQ) == 0 {
			return
		}
		if len(n.dioQ) > 0 && (i%2 == 0 || len(n.daoQ) == 0) {
			id := n.dioQ[0]
			n.dioQ = n.dioQ[1:]
			delete(n.inDIO, id)
			src := n.nodes[id]
			if src == nil || !src.Joined() {
				continue
			}
			n.dioSent++
			for _, pid := range n.sortedNeighbors(id) {
				peer := n.nodes[pid]
				if peer == nil {
					continue
				}
				oldPar, oldOk := peer.Parent()
				if peer.ReceiveDIO(id, src.Version, src.Seq, src.Rank, src.DTSN(), n.time) {
					n.log(pid, "dio from %d: rank=%d version=%d", id, src.Rank, src.Version)
					n.afterChange(pid, oldPar, oldOk)
				}
			}
			continue
		}
		// DAO 队列
		id := n.daoQ[0]
		n.daoQ = n.daoQ[1:]
		delete(n.inDAO, id)
		src := n.nodes[id]
		if src == nil || !src.Joined() || src.IsRoot() {
			continue
		}
		par, ok := src.Parent()
		if !ok {
			continue
		}
		parent := n.nodes[par]
		if parent == nil {
			continue
		}
		oldPar, oldOk := parent.Parent()
		if parent.ReceiveDAO(src.DAO()) {
			n.log(par, "dao from %d: %d entries", id, len(src.DAO().entries))
			n.afterChange(par, oldPar, oldOk)
		}
	}
	panic("mesh: propagation did not converge")
}

// afterChange 在节点路由状态变化后安排后续传播：广播 DIO、向父发 DAO；
// 若父节点发生切换，向旧父投递 no-path 以清除其保留的下行表项，
// 保证下行路径与新的上行结构一致。no-path 引起的上游变化递归处理，
// 递归深度受树高限制。
func (n *Network) afterChange(id NodeID, oldPar NodeID, oldOk bool) {
	n.enqueueDIO(id)
	n.enqueueDAO(id)
	node := n.nodes[id]
	if node == nil {
		return
	}
	newPar, newOk := node.Parent()
	if oldOk && (!newOk || newPar != oldPar) {
		old := n.nodes[oldPar]
		if old == nil {
			return
		}
		gp, gpOk := old.Parent()
		if old.ReceiveDAO(node.NoPathDAO()) {
			n.log(oldPar, "no-path from %d", id)
			n.afterChange(oldPar, gp, gpOk)
		}
	}
}

// Apply 施加一个事件到网络，并在事件引起的全部变化传播收敛后返回。
func (n *Network) Apply(ev Event) {
	n.applyNoPropagate(ev)
	n.propagate()
}

// applyNoPropagate 只施加事件本身而不传播，供 Simulator 批量施加
// 并发事件后统一收敛。
func (n *Network) applyNoPropagate(ev Event) {
	n.log(0, "event#%d %s a=%d b=%d cost=%d", ev.Seq, ev.Kind, ev.A, ev.B, ev.Cost)
	switch ev.Kind {
	case EvNodeJoin:
		n.applyJoin(ev.A)
	case EvNodeLeave:
		n.applyLeave(ev.A)
	case EvNodeRestart:
		n.applyRestart(ev.A)
	case EvLinkUp:
		n.applyLinkUp(ev.A, ev.B, ev.Cost)
	case EvLinkDown:
		n.applyLinkDown(ev.A, ev.B)
	case EvLinkQuality:
		n.applyLinkQuality(ev.A, ev.B, ev.Cost)
	case EvTick:
		n.applyTick(ev.Steps)
	}
}

// applyJoin 处理节点加入。相同 ID 重复加入按重启处理，保证结果确定。
func (n *Network) applyJoin(id NodeID) {
	if _, exists := n.nodes[id]; exists {
		n.log(id, "duplicate join treated as restart")
		n.applyRestart(id)
		return
	}
	nodeRNG := rand.New(rand.NewSource(n.seed ^ (int64(id)*2654435761 + 1)))
	node := NewNode(id, n.imin, n.imax, n.k, nodeRNG)
	n.nodes[id] = node
	if id == n.root {
		node.joinAsRoot(1)
		n.log(id, "joined as root version=1")
	} else {
		n.log(id, "joined")
	}
	// 让已有邻居向新节点通告，使其能接入。
	for _, p := range n.sortedNeighbors(id) {
		n.enqueueDIO(p)
	}
}

// applyLeave 处理节点离开：拆除其全部链路并让邻居修复。
func (n *Network) applyLeave(id NodeID) {
	if _, ok := n.nodes[id]; !ok {
		return
	}
	for _, p := range n.sortedNeighbors(id) {
		n.unlink(id, p)
		if peer := n.nodes[p]; peer != nil {
			n.repairAfterLinkLoss(peer, id)
		}
	}
	delete(n.nodes, id)
	n.log(id, "left")
}

// applyRestart 处理节点重启：化身序列号递增，路由状态清空，
// 物理链路保留并重新建立邻居表项，随后由邻居的 DIO 引导重新接入。
func (n *Network) applyRestart(id NodeID) {
	node := n.nodes[id]
	if node == nil {
		n.applyJoin(id)
		return
	}
	node.Restart()
	if id == n.root {
		node.joinAsRoot(node.Version + 1) // 根重启提升 DODAG 版本
		n.log(id, "restarted as root, version=%d", node.Version)
	} else {
		n.log(id, "restarted, seq=%d", node.Seq)
	}
	for _, p := range n.sortedNeighbors(id) {
		if c, ok := n.links[id][p]; ok {
			node.AddLink(p, c)
		}
		n.enqueueDIO(p) // 邻居向它通告当前结构
	}
	// 重启后已接入的节点（根）自身也要广播，让新版本/新化身传播出去。
	if node.Joined() {
		n.enqueueDIO(id)
	}
}

// applyLinkUp 建立链路并触发两端互相通告。
func (n *Network) applyLinkUp(a, b NodeID, cost Cost) {
	if n.nodes[a] == nil || n.nodes[b] == nil {
		return
	}
	n.link(a, b, cost)
	n.nodes[a].AddLink(b, cost)
	n.nodes[b].AddLink(a, cost)
	n.log(a, "link-up to %d cost=%d", b, cost)
	n.enqueueDIO(a)
	n.enqueueDIO(b)
}

// applyLinkDown 拆除链路，两端清理邻居与下行表项并修复。
func (n *Network) applyLinkDown(a, b NodeID) {
	if n.links[a] == nil {
		return
	}
	if _, ok := n.links[a][b]; !ok {
		return
	}
	n.unlink(a, b)
	n.log(a, "link-down to %d", b)
	if na := n.nodes[a]; na != nil {
		n.repairAfterLinkLoss(na, b)
	}
	if nb := n.nodes[b]; nb != nil {
		n.repairAfterLinkLoss(nb, a)
	}
}

// applyLinkQuality 更新链路代价，两端重算 Rank 并传播变化。
func (n *Network) applyLinkQuality(a, b NodeID, cost Cost) {
	if n.links[a] == nil {
		return
	}
	if _, ok := n.links[a][b]; !ok {
		return
	}
	n.link(a, b, cost)
	na, nb := n.nodes[a], n.nodes[b]
	if na == nil || nb == nil {
		return
	}
	oa, oka := na.Parent()
	ob, okb := nb.Parent()
	if na.UpdateLink(b, cost) {
		n.afterChange(a, oa, oka)
	}
	if nb.UpdateLink(a, cost) {
		n.afterChange(b, ob, okb)
	}
	n.log(a, "link-quality to %d cost=%d", b, cost)
}

// applyTick 推进仿真时钟，Trickle 到期的节点广播 DIO。
func (n *Network) applyTick(steps int) {
	if steps <= 0 {
		steps = 1
	}
	n.time += uint64(steps)
	for _, id := range n.Nodes() {
		if n.nodes[id].Tick(uint64(steps)) {
			n.enqueueDIO(id)
		}
	}
}

// repairAfterLinkLoss 在链路丢失后清理本地状态并安排修复传播。
func (n *Network) repairAfterLinkLoss(node *Node, peer NodeID) {
	oldPar, oldOk := node.Parent()
	if node.RemoveLink(peer) {
		n.afterChange(node.ID, oldPar, oldOk)
	}
}

// link / unlink 维护无向链路表。
func (n *Network) link(a, b NodeID, c Cost) {
	if n.links[a] == nil {
		n.links[a] = make(map[NodeID]Cost)
	}
	if n.links[b] == nil {
		n.links[b] = make(map[NodeID]Cost)
	}
	n.links[a][b] = c
	n.links[b][a] = c
}

func (n *Network) unlink(a, b NodeID) {
	delete(n.links[a], b)
	delete(n.links[b], a)
}

// CheckAcyclic 校验当前上行结构无环；发现环时返回环上的节点列表。
func (n *Network) CheckAcyclic() (bool, []NodeID) {
	for _, start := range n.Nodes() {
		visited := make(map[NodeID]bool)
		cur := n.nodes[start]
		for cur != nil && !cur.IsRoot() {
			par, ok := cur.Parent()
			if !ok {
				break
			}
			if visited[par] {
				cycle := []NodeID{par}
				for c := n.nodes[par]; ; {
					p, _ := c.Parent()
					cycle = append(cycle, p)
					if p == par {
						break
					}
					c = n.nodes[p]
				}
				return false, cycle
			}
			visited[cur.ID] = true
			cur = n.nodes[par]
		}
	}
	return true, nil
}

// Converged 报告网络是否已收敛：传播队列排空，且每个在线节点
// 都能给出到达根节点的有效路径（根自身除外）。
func (n *Network) Converged() bool {
	if len(n.dioQ) > 0 || len(n.daoQ) > 0 {
		return false
	}
	for _, id := range n.Nodes() {
		node := n.nodes[id]
		if !node.Joined() {
			return false
		}
		if node.IsRoot() {
			continue
		}
		if _, err := node.ForwardUp(); err != nil {
			return false
		}
	}
	return true
}
