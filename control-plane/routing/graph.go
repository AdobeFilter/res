package routing

import (
	"valhalla/common/api"
)

// Edge represents a weighted connection between two nodes.
type Edge struct {
	From   string
	To     string
	Cost   float64
}

// Graph represents the mesh network topology with weighted edges.
type Graph struct {
	nodes    map[string]*api.NodeInfo
	edges    map[string][]Edge
	weights  Weights
}

// BuildGraph constructs a weighted graph from online nodes and their metrics.
// A direct edge is added between two nodes only when at least one has a
// publicly reachable Endpoint — otherwise neither side can dial the other,
// so a direct WG handshake is impossible and the route service must fall
// through to relay. Without this check, two NAT-bound clients would always
// resolve to ConnectionDirect, never get relay credentials, and fail to
// connect.
func BuildGraph(nodes []*api.NodeInfo, metrics map[string]*api.Metrics, optimizer *Optimizer) *Graph {
	g := &Graph{
		nodes:   make(map[string]*api.NodeInfo),
		edges:   make(map[string][]Edge),
		weights: optimizer.weights,
	}

	for _, n := range nodes {
		g.nodes[n.ID] = n
	}

	for i, a := range nodes {
		for j, b := range nodes {
			if i >= j {
				continue
			}
			// Client↔client never gets a direct edge: that pair always
			// routes through the relay in the MVP (one of them is almost
			// always behind a symmetric NAT or a DPI-ed network where a
			// plain WG handshake won't hold). This keeps client-to-exit
			// edges for the existing internet-egress Dijkstra.
			if a.NodeType == api.NodeTypeClient && b.NodeType == api.NodeTypeClient {
				continue
			}
			if a.Endpoint == "" && b.Endpoint == "" {
				continue
			}

			costA := Cost(metrics[a.ID], g.weights)
			costB := Cost(metrics[b.ID], g.weights)
			avgCost := (costA + costB) / 2.0

			g.edges[a.ID] = append(g.edges[a.ID], Edge{From: a.ID, To: b.ID, Cost: avgCost})
			g.edges[b.ID] = append(g.edges[b.ID], Edge{From: b.ID, To: a.ID, Cost: avgCost})
		}
	}

	return g
}

// Neighbors returns all edges from a given node.
func (g *Graph) Neighbors(nodeID string) []Edge {
	return g.edges[nodeID]
}

// NodeCount returns the number of nodes in the graph.
func (g *Graph) NodeCount() int {
	return len(g.nodes)
}
