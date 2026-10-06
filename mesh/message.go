package mesh

// MsgType 区分控制消息与数据报文。
type MsgType int

const (
	// MsgDIO 拓扑信息广播（上行结构维护）：携带发送者的 Rank、版本与代次。
	MsgDIO MsgType = iota
	// MsgDAO 下行路由通告：向父节点登记“我及我的子树”。
	MsgDAO
	// MsgDAOAck 父节点对 DAO 的确认，用于切换父节点时的状态一致性。
	MsgDAOAck
	// MsgDataUp 上行数据报文（节点 -> 根）。
	MsgDataUp
	// MsgDataDown 下行数据报文（根 -> 节点）。
	MsgDataDown
	// MsgNACK 转发失败时回送给发送方的否定确认，携带可区分原因。
	MsgNACK
)

func (t MsgType) String() string {
	switch t {
	case MsgDIO:
		return "DIO"
	case MsgDAO:
		return "DAO"
	case MsgDAOAck:
		return "DAO-ACK"
	case MsgDataUp:
		return "DATA-UP"
	case MsgDataDown:
		return "DATA-DOWN"
	case MsgNACK:
		return "NACK"
	default:
		return "UNKNOWN"
	}
}

// Msg 是节点间传递的消息。所有消息都携带发送者的拓扑代次信息，
// 接收方据此识别并丢弃陈旧消息。
type Msg struct {
	Type MsgType
	From NodeID
	To   NodeID // 单播目的；广播时为 InvalidNodeID

	// 控制字段（DIO/DAO 使用）
	Version Version     // 发送者所在拓扑版本
	Seq     SeqNo       // 发送者自身代次序列号
	Rank    Rank        // 发送者当前 Rank
	PathSeq SeqNo       // 下行路径序列号（DAO 用，单调递增）
	AdvDest NodeID      // DAO 通告的目的节点（子树根）
	AdvSeq  SeqNo       // 目的节点的代次（识别陈旧路由）
	AdvVia  NodeID      // 通告者（即发送者）
	AdvCost LinkQuality // 通告者到 AdvDest 的代价；>= poisonCost 表示撤销路由

	// 数据字段
	PayloadID uint64 // 报文 ID，用于跟踪与日志
	Src       NodeID // 原始发送者
	Dst       NodeID // 最终目的地
	TTL       int    // 剩余跳数，耗尽判定为环路
	NACKCause ForwardErr
}
