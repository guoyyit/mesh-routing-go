package mesh

// Simulator 以固定随机种子按序施加事件，产生逐事件一致的输出。
//
// 同一批次（Batch）内的事件先全部施加、再统一传播收敛，用于模拟
// 并发产生的拓扑事件：批内事件互看不到对方的中间状态，最终结果
// 只取决于批内容与批顺序，与批内排列无关的确定性由排序保证。
type Simulator struct {
	net     *Network
	batches [][]Event
	seq     uint64
}

// NewSimulator 创建模拟器；events 中的每个事件独立成批。
func NewSimulator(net *Network, events []Event) *Simulator {
	batches := make([][]Event, len(events))
	for i, ev := range events {
		batches[i] = []Event{ev}
	}
	return &Simulator{net: net, batches: batches}
}

// NewBatchSimulator 创建按批施加事件的模拟器，用于并发拓扑事件场景。
func NewBatchSimulator(net *Network, batches [][]Event) *Simulator {
	return &Simulator{net: net, batches: batches}
}

// Run 依次施加全部批次，返回逐事件日志。
// 每个批次施加后记录一条收敛摘要，便于复现拓扑收敛与异常判定过程。
func (s *Simulator) Run() []LogEntry {
	for _, batch := range s.batches {
		// 批内事件先全部施加（不传播），模拟并发发生。
		for _, ev := range batch {
			s.seq++
			ev.Seq = s.seq
			s.net.applyNoPropagate(ev)
		}
		// 统一传播收敛。
		s.net.propagate()
		ok, cycle := s.net.CheckAcyclic()
		if !ok {
			s.net.log(0, "ANOMALY cycle detected: %v", cycle)
		}
		s.net.log(0, "batch done: converged=%v acyclic=%v", s.net.Converged(), ok)
	}
	return s.net.Logs()
}
