// Package mesh 实现本地多跳自组网路由与拓扑修复能力。
//
// 协议模型参考 RPL：节点自组织成以根节点为汇的 DODAG（面向目的地的有向无环图），
// 上行沿父节点汇聚到根，下行沿路由通告建立的反向路径下发。
package mesh

import "fmt"

// NodeID 是节点的唯一标识。
type NodeID uint32

// InvalidNodeID 表示无效节点。
const InvalidNodeID NodeID = 0

// Rank 表示节点在 DODAG 中的位置度量，数值越小越靠近根。
// 父节点的 Rank 必须严格小于子节点，以此保证无环。
type Rank uint32

const (
	// RootRank 是根节点的 Rank。
	RootRank Rank = 1
	// InfiniteRank 表示节点未接入任何拓扑（不可达根）。
	InfiniteRank = Rank(^uint32(0))
)

// Version 标识一次拓扑版本。根节点重建拓扑时递增，
// 节点重启后必须携带新的身份代次重新加入，旧版本状态一律视为陈旧。
type Version uint32

// SeqNo 是节点自身状态的序列号，重启后递增，
// 用于区分同一 NodeID 的新旧化身（incarnation）。
type SeqNo uint32

// LinkQuality 是链路质量度量（类似 ETX 的期望传输次数，>= 1.0，越小越好）。
type LinkQuality float64

const (
	// LinkDown 表示链路不可用。
	LinkDown LinkQuality = 0
	// LinkPerfect 是理想链路质量。
	LinkPerfect LinkQuality = 1.0
	// MaxUsableLink 是可用来选父的链路质量上限（超过则认为链路太差不可用）。
	MaxUsableLink LinkQuality = 16.0
)

// ForwardErr 是转发失败的可区分原因码。
type ForwardErr int

const (
	// ForwardOK 转发成功。
	ForwardOK ForwardErr = iota
	// ErrNoUpwardRoute 没有可用的上行路由（未接入拓扑或父节点失效）。
	ErrNoUpwardRoute
	// ErrNoDownwardRoute 没有到达目的节点的下行路由。
	ErrNoDownwardRoute
	// ErrParentSwitching 父节点正在切换，旧父已弃用、新父未确认，暂不可转发。
	ErrParentSwitching
	// ErrLoopDetected 检测到环路，报文被丢弃并触发拓扑修复。
	ErrLoopDetected
	// ErrStaleState 报文携带的状态与当前拓扑版本/代次不符。
	ErrStaleState
	// ErrDestinationDown 目的节点已离开网络。
	ErrDestinationDown
)

func (e ForwardErr) String() string {
	switch e {
	case ForwardOK:
		return "ok"
	case ErrNoUpwardRoute:
		return "no-upward-route"
	case ErrNoDownwardRoute:
		return "no-downward-route"
	case ErrParentSwitching:
		return "parent-switching"
	case ErrLoopDetected:
		return "loop-detected"
	case ErrStaleState:
		return "stale-state"
	case ErrDestinationDown:
		return "destination-down"
	default:
		return fmt.Sprintf("unknown(%d)", int(e))
	}
}

// Path 表示一条到达根节点（上行）或到达某节点（下行）的路径。
type Path struct {
	// Hops 是从本节点到目的地的逐跳节点序列（含本节点与目的地）。
	Hops []NodeID
	// Cost 是路径总代价（各跳链路质量之和）。
	Cost LinkQuality
}

// Valid 报告路径是否可用。
func (p Path) Valid() bool { return len(p.Hops) > 0 }
