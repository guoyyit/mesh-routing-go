# mesh-routing-go

面向资源受限节点的本地多跳自组网路由与拓扑修复实现。节点自组织成以根节点为汇的
DODAG（面向目的地的有向无环图），在链路频繁抖动下仍能稳定地把数据汇聚到根节点，
并支持从根节点向任意可达节点下发指令。

协议模型参考 RPL（RFC 6550）与 Trickle（RFC 6206），在事件驱动的确定性模拟器上运行。

## 路由语义

### 上行（汇聚）

- 每个节点维护一个**首选父节点**，上行报文沿父链逐跳转发到根。
- `Rank` 表示节点在拓扑中的深度：根为 `RootRank=1`，节点 Rank = 父 Rank + 1。
- 父节点选择标准：候选中 `父Rank + 链路代价` 最小者，平局取 NodeID 小者（确定性）。
- `Node.UpwardPath()` 返回到根的一条可用路径及总代价；未接入时返回无效路径。

### 下行（下发）

- 节点通过 **DAO** 向父节点登记“自己及子树”，沿途节点记录下行路由表
  `目的 -> 下一跳`，直到根。
- 下行路径必然与当前上行结构一致（互为逆路径）；结构变化后 DAO 沿新父链重新登记，
  在有限时间内收敛。
- 换父时向旧父发送 **poison DAO**（代价为 `poisonCost`），显式撤销旧路径上的下行路由。

### 无环保证

任意时刻路由结构无环，由三条规则共同保证：

| 规则 | 内容 |
|------|------|
| R1 | 父的 Rank 严格小于自身 Rank（选父时构造保证，收到父 DIO 时校验） |
| R2 | 不得选择下行路由表中的子孙作为父节点 |
| R3 | 数据面校验报文 Rank/TTL，发现不一致立即拒绝并触发本地修复 |

### 转发失败语义（不静默丢弃）

`ForwardErr` 返回可区分原因：

| 原因 | 含义 |
|------|------|
| `no-upward-route` | 无可用上行路由（未接入或父失效） |
| `parent-switching` | 父节点切换中（暂时性故障，有候选未收敛） |
| `no-downward-route` | 无到达目的节点的下行路由 |
| `destination-down` | 目的节点已离开网络 |
| `loop-detected` | 检测到环路，报文被拒并触发修复 |
| `stale-state` | 报文携带陈旧版本/代次 |

失败时向源节点回送 NACK（带内通知）并记录结构化日志。

### 控制消息自适应抑制（Trickle）

- 拓扑稳定：发送间隔指数退避 `Imin -> Imax`，控制开销明显下降。
- 拓扑变化（不一致事件）：间隔立即重置为 `Imin`，更新及时传播。
- 一个间隔内收到 `K` 个一致通告则抑制本次发送（去冗余）。
- 抑制窗口内的重要变化通过 `Reset` 打破抑制，不会被吞掉。

### 重启与陈旧状态

- 每个 NodeID 维护单调递增的**代次 SeqNo**，重启时递增；旧代次的消息与路由一律作废。
- 根节点重启时**拓扑版本 Version** 递增，全网旧版本状态被识别并清空重建。
- 同一身份重复出现时结果确定：代次/版本大者胜。

### 确定性模拟

`Network` 是事件驱动模拟器：时间以 tick 离散推进，消息在产生它的 tick 内同步投递。
固定随机种子（Trickle 发送时刻的唯一随机源）+ 相同事件序列 => 逐事件一致的日志输出，
可复现拓扑收敛与异常判定过程。

## 配置项

```go
type Config struct {
    Seed        int64  // 固定随机种子（默认 1）
    TrickleImin uint64 // Trickle 最小间隔，tick（默认 2）
    TrickleImax uint64 // Trickle 最大间隔，tick（默认 64）
    TrickleK    int    // 冗余抑制常数（默认 2）
}
```

链路质量 `LinkQuality` 为期望传输次数语义：`1.0` 为理想链路，`<= 0` 表示断开，
超过 `MaxUsableLink=16` 不可用于选父。

## 使用示例

```go
net := mesh.NewNetwork(mesh.DefaultConfig())
net.Submit(mesh.Event{Time: 0, Type: mesh.EvNodeJoin, A: 1, IsRoot: true})
net.Submit(mesh.Event{Time: 0, Type: mesh.EvNodeJoin, A: 2})
net.Submit(mesh.Event{Time: 1, Type: mesh.EvLinkChange, A: 1, B: 2, Q: 1})
net.Run()
net.Advance(20) // 推进 20 tick 让拓扑收敛

n2 := net.Get(2)
path := n2.UpwardPath()      // 到根的路径与代价
err := n2.SendUp(1)          // 上行发送，返回可区分结果
```

## 事件类型

| 事件 | 参数 | 语义 |
|------|------|------|
| `EvNodeJoin` | A, IsRoot | 节点加入（重复加入视为新化身） |
| `EvNodeLeave` | A | 节点离开 |
| `EvNodeRestart` | A | 节点重启（代次递增，物理链路保留） |
| `EvLinkChange` | A, B, Q | 双向链路质量变化，Q<=0 断开 |
| `EvSendUp` | A | 从 A 向根发送上行数据 |
| `EvSendDown` | A | 从根向 A 发送下行数据 |

## 测试

```sh
go test ./mesh/ -v
```

覆盖：拓扑收敛、上下行数据面、父节点切换、错误原因区分、节点离开重挂、
环路检测、节点重启、陈旧代次隔离、链路抖动、确定性重放、Trickle 抑制、
并发拓扑事件、下行/上行一致性。
