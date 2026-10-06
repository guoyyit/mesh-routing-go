package mesh

import (
	"fmt"
	"math/rand"
	"sort"
)

// EventType 是模拟器支持的外部事件类型。
type EventType int

const (
	// EvNodeJoin 节点加入网络。
	EvNodeJoin EventType = iota
	// EvNodeLeave 节点离开网络。
	EvNodeLeave
	// EvNodeRestart 节点重启（代次递增，旧状态作废）。
	EvNodeRestart
	// EvLinkChange 链路质量变化（q <= 0 表示链路断开）。
	EvLinkChange
	// EvSendUp 从某节点向根发送上行数据。
	EvSendUp
	// EvSendDown 从根向某节点发送下行数据。
	EvSendDown
)

// Event 描述一次注入模拟器的外部事件。
type Event struct {
	Time   uint64 // 事件发生时刻（tick）
	Type   EventType
	A, B   NodeID      // 节点参数（链路事件为两端）
	Q      LinkQuality // 链路质量（EvLinkChange）
	IsRoot bool        // 是否为根节点（EvNodeJoin）
}

// LogEntry 是模拟过程的一条结构化日志，用于确定性比对。
type LogEntry struct {
	Time   uint64
	Node   NodeID
	Action string
	Detail string
}

// Config 是模拟器与协议参数。
type Config struct {
	Seed int64 // 固定随机种子，保证可复现

	TrickleImin uint64 // Trickle 最小间隔（tick）
	TrickleImax uint64 // Trickle 最大间隔（tick）
	TrickleK    int    // Trickle 冗余抑制常数
}

// DefaultConfig 返回一组可用的默认参数。
func DefaultConfig() Config {
	return Config{
		Seed:        1,
		TrickleImin: 2,
		TrickleImax: 64,
		TrickleK:    2,
	}
}

// Network 是事件驱动的确定性网络模拟器：
// 固定随机种子 + 相同事件序列 => 逐事件一致输出。
// 时间以 tick 为单位离散推进，消息在产生它的 tick 内同步投递。
type Network struct {
	rng         *rand.Rand
	now         uint64
	nodes       map[NodeID]*Node
	links       map[NodeID]map[NodeID]LinkQuality
	events      []Event
	logs        []LogEntry
	msgSeq      uint64
	root        NodeID
	rootVersion Version
	seqs        map[NodeID]SeqNo // 每个 NodeID 的代次计数器（重启递增）
	cfg         Config
}

// NewNetwork 创建模拟器。
func NewNetwork(cfg Config) *Network {
	if cfg.TrickleImin == 0 {
		cfg = DefaultConfig()
	}
	return &Network{
		rng:   rand.New(rand.NewSource(cfg.Seed)),
		nodes: make(map[NodeID]*Node),
		links: make(map[NodeID]map[NodeID]LinkQuality),
		seqs:  make(map[NodeID]SeqNo),
		cfg:   cfg,
	}
}

// ---------- 成员与链路管理 ----------

// AddNode 将一个节点加入网络。同一 NodeID 重复加入视为新化身：
// 代次单调递增，旧化身状态一律作废，结果确定（代次大者胜）。
func (n *Network) AddNode(id NodeID, isRoot bool) *Node {
	n.seqs[id]++
	seq := n.seqs[id]
	if isRoot {
		n.rootVersion++
		n.root = id
	}
	nd := newNode(n, id, isRoot, seq, n.rootVersion)
	n.nodes[id] = nd
	n.logf(id, "join", fmt.Sprintf("seq=%d root=%v ver=%d", seq, isRoot, n.rootVersion))
	// 模拟 DIS：促使已接入邻居尽快广播 DIO，加速新节点接入。
	for _, nbID := range n.sortedNeighbors(id) {
		if nb := n.nodes[nbID]; nb != nil && nb.Attached() {
			nb.trickle.Reset()
		}
	}
	return nd
}

// RemoveNode 将节点移出网络，并通知所有邻居链路断开。
func (n *Network) RemoveNode(id NodeID) {
	if n.nodes[id] == nil {
		return
	}
	for _, nbID := range n.sortedNeighbors(id) {
		delete(n.links[nbID], id)
		if nb := n.nodes[nbID]; nb != nil {
			nb.OnLinkChange(id, LinkDown)
		}
	}
	delete(n.links, id)
	delete(n.nodes, id)
	n.logf(id, "leave", "")
}

// RestartNode 重启节点：旧化身离开，同一代次递增的新化身加入。
// 重启只清除协议状态，物理链路在重启后重新建立（介质层面不受影响）。
func (n *Network) RestartNode(id NodeID) {
	nd := n.nodes[id]
	if nd == nil {
		return
	}
	isRoot := nd.isRoot
	saved := make(map[NodeID]LinkQuality, len(n.links[id]))
	for nb, q := range n.links[id] {
		saved[nb] = q
	}
	n.logf(id, "restart", "")
	n.RemoveNode(id)
	n.AddNode(id, isRoot)
	for _, nb := range sortedIDs(saved) {
		n.SetLink(id, nb, saved[nb])
	}
}

// SetLink 设置双向链路质量；q <= 0 表示断开。
func (n *Network) SetLink(a, b NodeID, q LinkQuality) {
	if q <= LinkDown {
		delete(n.links[a], b)
		delete(n.links[b], a)
	} else {
		if n.links[a] == nil {
			n.links[a] = make(map[NodeID]LinkQuality)
		}
		if n.links[b] == nil {
			n.links[b] = make(map[NodeID]LinkQuality)
		}
		n.links[a][b] = q
		n.links[b][a] = q
	}
	n.logf(a, "link", fmt.Sprintf("peer=%d q=%.1f", b, q))
	if nd := n.nodes[a]; nd != nil {
		nd.OnLinkChange(b, q)
	}
	if nd := n.nodes[b]; nd != nil {
		nd.OnLinkChange(a, q)
	}
}

// Link 查询链路质量，不存在时返回 LinkDown。
func (n *Network) Link(a, b NodeID) LinkQuality {
	if m := n.links[a]; m != nil {
		if q, ok := m[b]; ok {
			return q
		}
	}
	return LinkDown
}

// Get 返回节点，不存在时返回 nil。
func (n *Network) Get(id NodeID) *Node { return n.nodes[id] }

// Root 返回当前根节点 ID。
func (n *Network) Root() NodeID { return n.root }

// ---------- 事件驱动主循环 ----------

// Submit 注入一个外部事件。
func (n *Network) Submit(ev Event) { n.events = append(n.events, ev) }

// Run 按 (时间, 提交顺序) 处理全部事件，返回结构化日志。
func (n *Network) Run() []LogEntry {
	sorted := make([]Event, len(n.events))
	copy(sorted, n.events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time < sorted[j].Time })
	for _, ev := range sorted {
		if ev.Time > n.now {
			n.Advance(ev.Time - n.now)
		}
		n.handleEvent(ev)
	}
	return n.logs
}

// Advance 推进指定数量的 tick；每个 tick 内所有节点按 ID 序推进 Trickle。
func (n *Network) Advance(ticks uint64) {
	for t := uint64(0); t < ticks; t++ {
		n.now++
		for _, id := range n.sortedNodeIDs() {
			if nd := n.nodes[id]; nd != nil {
				nd.tick()
			}
		}
	}
}

// Now 返回当前模拟时刻。
func (n *Network) Now() uint64 { return n.now }

// Logs 返回当前已产生的日志。
func (n *Network) Logs() []LogEntry { return n.logs }

// handleEvent 处理一个外部事件。
func (n *Network) handleEvent(ev Event) {
	switch ev.Type {
	case EvNodeJoin:
		n.AddNode(ev.A, ev.IsRoot)
	case EvNodeLeave:
		n.RemoveNode(ev.A)
	case EvNodeRestart:
		n.RestartNode(ev.A)
	case EvLinkChange:
		n.SetLink(ev.A, ev.B, ev.Q)
	case EvSendUp:
		nd := n.nodes[ev.A]
		if nd == nil {
			n.logf(ev.A, "send-up", "node-absent")
			return
		}
		r := nd.SendUp(n.nextMsgID())
		n.logf(ev.A, "send-up", "result="+r.String())
	case EvSendDown:
		root := n.nodes[n.root]
		if root == nil {
			n.logf(ev.A, "send-down", "no-root")
			return
		}
		r := root.SendDown(ev.A, n.nextMsgID())
		n.logf(n.root, "send-down", fmt.Sprintf("dst=%d result=%s", ev.A, r))
	}
}

// ---------- 消息投递（同步、同 tick 内完成） ----------

func (n *Network) deliverDIO(m Msg) {
	if n.Link(m.From, m.To) <= LinkDown {
		return
	}
	n.logf(m.From, "dio", fmt.Sprintf("to=%d rank=%d ver=%d seq=%d", m.To, m.Rank, m.Version, m.Seq))
	if nd := n.nodes[m.To]; nd != nil {
		nd.OnDIO(m)
	}
}

func (n *Network) deliverDAO(m Msg) {
	if n.Link(m.From, m.To) <= LinkDown {
		return
	}
	if nd := n.nodes[m.To]; nd != nil {
		nd.OnDAO(m)
	}
}

func (n *Network) deliverDAOAck(m Msg) {
	if n.Link(m.From, m.To) <= LinkDown {
		return
	}
	if nd := n.nodes[m.To]; nd != nil {
		nd.OnDAOAck(m)
	}
}

// ---------- 内部工具 ----------

// logf 追加一条结构化日志。
func (n *Network) logf(node NodeID, action, detail string) {
	n.logs = append(n.logs, LogEntry{Time: n.now, Node: node, Action: action, Detail: detail})
}

// nextMsgID 分配单调递增的报文 ID（确定性）。
func (n *Network) nextMsgID() uint64 {
	n.msgSeq++
	return n.msgSeq
}

// sortedNeighbors 返回按 ID 升序的邻居列表（保证遍历确定性）。
func (n *Network) sortedNeighbors(id NodeID) []NodeID {
	m := n.links[id]
	out := make([]NodeID, 0, len(m))
	for nb := range m {
		out = append(out, nb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortedNodeIDs 返回按 ID 升序的节点列表。
func (n *Network) sortedNodeIDs() []NodeID {
	out := make([]NodeID, 0, len(n.nodes))
	for id := range n.nodes {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortedIDs 返回 map 键的升序列表（保证遍历确定性）。
func sortedIDs(m map[NodeID]LinkQuality) []NodeID {
	out := make([]NodeID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
