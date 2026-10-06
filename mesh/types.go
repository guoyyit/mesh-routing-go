// Package mesh 实现本地多跳自组网路由与拓扑修复。
// 路由结构为一棵以根节点为汇聚点的 DODAG（有向无环图），
// 语义参考 RPL（RFC 6550）的核心思想并做了本地简化。
package mesh

import "fmt"

// NodeID 是节点的唯一标识。
type NodeID uint32

// Cost 表示链路或路径代价，使用放大 256 倍的 ETX 风格度量。
type Cost uint32

// InfiniteCost 表示不可达。
const InfiniteCost = Cost(^uint32(0))

// RootCost 是根节点到自身的代价。
const RootCost = Cost(0)

// ForwardReason 是无法转发报文时可区分的原因码。
type ForwardReason int

const (
	// FwdOK 表示转发成功。
	FwdOK ForwardReason = iota
	// FwdNoRoute 表示本地没有任何到达根/目的地的路由。
	FwdNoRoute
	// FwdParentMissing 表示记录的父节点已不在邻居表中（链路断开尚未修复）。
	FwdParentMissing
	// FwdLoopDetected 表示转发会构成环路，报文被拒绝。
	FwdLoopDetected
	// FwdStaleState 表示本节点状态陈旧（如重启后尚未重新接入）。
	FwdStaleState
	// FwdUnreachable 表示下行目的节点当前不可达。
	FwdUnreachable
)

func (r ForwardReason) String() string {
	switch r {
	case FwdOK:
		return "ok"
	case FwdNoRoute:
		return "no-route"
	case FwdParentMissing:
		return "parent-missing"
	case FwdLoopDetected:
		return "loop-detected"
	case FwdStaleState:
		return "stale-state"
	case FwdUnreachable:
		return "unreachable"
	default:
		return "unknown"
	}

}

// ForwardError 携带可区分原因的转发失败信息。
type ForwardError struct {
	Reason ForwardReason
	Detail string
}

func (e *ForwardError) Error() string {
	return fmt.Sprintf("forward failed: %s (%s)", e.Reason, e.Detail)
}

// PathInfo 描述一条到达根节点（或目的节点）的可用路径。
type PathInfo struct {
	NextHop NodeID // 下一跳
	Cost    Cost   // 整条路径代价
	Hops    int    // 跳数
}
