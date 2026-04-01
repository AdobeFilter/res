package routing

import (
	"container/heap"
	"fmt"
	"time"

	"valhalla/common/api"
)

// Optimizer runs Dijkstra's algorithm on the node graph.
type Optimizer struct {
	weights Weights
}

func NewOptimizer(w Weights) *Optimizer {
	return &Optimizer{weights: w}
}

// ShortestPath finds the lowest-cost path from src to dst using Dijkstra.
func (g *Graph) ShortestPath(srcID, dstID string) (*api.Route, error) {
	if _, ok := g.nodes[srcID]; !ok {
		return nil, fmt.Errorf("source node %s not in graph", srcID)
	}
	if _, ok := g.nodes[dstID]; !ok {
		return nil, fmt.Errorf("destination node %s not in graph", dstID)
	}

	dist := make(map[string]float64)
	prev := make(map[string]string)
	for id := range g.nodes {
		dist[id] = 1e18
	}
	dist[srcID] = 0

	pq := &priorityQueue{}
	heap.Init(pq)
	heap.Push(pq, &pqItem{nodeID: srcID, cost: 0})

	for pq.Len() > 0 {
		item := heap.Pop(pq).(*pqItem)

		if item.nodeID == dstID {
			break
		}

		if item.cost > dist[item.nodeID] {
			continue
		}

		for _, edge := range g.Neighbors(item.nodeID) {
			newCost := dist[item.nodeID] + edge.Cost
			if newCost < dist[edge.To] {
				dist[edge.To] = newCost
				prev[edge.To] = item.nodeID
				heap.Push(pq, &pqItem{nodeID: edge.To, cost: newCost})
			}
		}
	}

	if dist[dstID] >= 1e18 {
		return nil, fmt.Errorf("no path from %s to %s", srcID, dstID)
	}

	// Reconstruct path
	var path []string
	for cur := dstID; cur != ""; cur = prev[cur] {
		path = append([]string{cur}, path...)
		if cur == srcID {
			break
		}
	}

	// Determine connection type
	connType := api.ConnectionDirect
	var relayNodeID string
	if len(path) > 2 {
		connType = api.ConnectionRelay
		// The middle node is the relay
		relayNodeID = path[1]
	}

	return &api.Route{
		SrcNodeID:      srcID,
		DstNodeID:      dstID,
		Path:           path,
		Cost:           dist[dstID],
		ConnectionType: connType,
		RelayNodeID:    relayNodeID,
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(60 * time.Second),
	}, nil
}

// --- Priority Queue for Dijkstra ---

type pqItem struct {
	nodeID string
	cost   float64
	index  int
}

type priorityQueue []*pqItem

func (pq priorityQueue) Len() int            { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool  { return pq[i].cost < pq[j].cost }
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x interface{}) {
	item := x.(*pqItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}

func (pq *priorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*pq = old[:n-1]
	return item
}
