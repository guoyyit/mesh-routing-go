package mesh

// EventKind 是拓扑/仿真事件的类型。
type EventKind int

const (
	// EvNodeJoin 节点加入网络。
	EvNodeJoin EventKind = iota
	// EvNodeLeave 节点离开网络。
	EvNodeLeave
	// EvNodeRestart 节点重启（状态清空后重新加入）。
	EvNodeRestart
	// EvLinkUp 链路出现。
	EvLinkUp
	// EvLinkDown 链路消失。
	EvLinkDown
	// EvLinkQuality 链路质量变化（Cost 字段为新代价）。
	EvLinkQuality
	// EvTick 仿真时钟推进，用于触发控制消息与收敛检查。
	EvTick
)

func (k EventKind) String() string {
	switch k {
	case EvNodeJoin:
		return "node-join"
	case EvNodeLeave:
		return "node-leave"
	case EvNodeRestart:
		return "node-restart"
	case EvLinkUp:
		return "link-up"
	case EvLinkDown:
		return "link-down"
	case EvLinkQuality:
		return "link-quality"
	case EvTick:
		return "tick"
	default:
		return "unknown"
	}
}

// Event 描述一个按序施加到网络上的事件。
type Event struct {
	Seq   uint64    // 事件序号（注入顺序）
	Kind  EventKind // 事件类型
	A, B  NodeID    // 涉及的节点（链路事件为两端）
	Cost  Cost      // 链路代价（EvLinkUp / EvLinkQuality 有效）
	Steps int       // EvTick 推进的时钟步数
}

// LogEntry 是逐事件输出的一条记录，用于确定性复现。
type LogEntry struct {
	Seq     uint64 // 触发该记录的事件序号
	Time    uint64 // 仿真时钟
	Node    NodeID // 相关节点
	Message string // 人类可读的描述
}
