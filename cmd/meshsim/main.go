// meshsim 演示多跳自组网的收敛、抖动修复与上下行数据面。
package main

import (
	"fmt"

	mesh "github.com/guoyyit/mesh-routing-go/mesh"
)

func main() {
	cfg := mesh.DefaultConfig()
	cfg.Seed = 42
	net := mesh.NewNetwork(cfg)

	// 组网：1 为根，2..5 为普通节点。
	for id := mesh.NodeID(1); id <= 5; id++ {
		net.Submit(mesh.Event{Time: 0, Type: mesh.EvNodeJoin, A: id, IsRoot: id == 1})
	}
	net.Submit(mesh.Event{Time: 1, Type: mesh.EvLinkChange, A: 1, B: 2, Q: 1})
	net.Submit(mesh.Event{Time: 1, Type: mesh.EvLinkChange, A: 2, B: 3, Q: 1})
	net.Submit(mesh.Event{Time: 1, Type: mesh.EvLinkChange, A: 3, B: 4, Q: 1})
	net.Submit(mesh.Event{Time: 1, Type: mesh.EvLinkChange, A: 1, B: 5, Q: 2})
	net.Submit(mesh.Event{Time: 1, Type: mesh.EvLinkChange, A: 5, B: 4, Q: 3})

	// 收敛后跑一次上下行。
	net.Submit(mesh.Event{Time: 30, Type: mesh.EvSendUp, A: 4})
	net.Submit(mesh.Event{Time: 31, Type: mesh.EvSendDown, A: 4})

	// 链路抖动：2-3 断开，4 号节点的子树经 5 号重挂。
	net.Submit(mesh.Event{Time: 40, Type: mesh.EvLinkChange, A: 2, B: 3, Q: 0})
	net.Submit(mesh.Event{Time: 60, Type: mesh.EvSendUp, A: 4})
	net.Submit(mesh.Event{Time: 61, Type: mesh.EvSendDown, A: 4})

	// 节点 3 重启。
	net.Submit(mesh.Event{Time: 80, Type: mesh.EvNodeRestart, A: 3})
	net.Submit(mesh.Event{Time: 110, Type: mesh.EvSendUp, A: 4})

	net.Run()
	net.Advance(150)

	// 打印关键事件日志（过滤掉高频 DIO）。
	for _, l := range net.Logs() {
		if l.Action == "dio" {
			continue
		}
		fmt.Printf("t=%-4d node=%-2d %-14s %s\n", l.Time, l.Node, l.Action, l.Detail)
	}

	fmt.Println("\n最终拓扑：")
	for _, id := range []mesh.NodeID{2, 3, 4, 5} {
		n := net.Get(id)
		p := n.UpwardPath()
		fmt.Printf("  node=%d parent=%d rank=%d path=%v cost=%.1f\n",
			id, n.Parent(), n.Rank(), p.Hops, p.Cost)
	}
}
