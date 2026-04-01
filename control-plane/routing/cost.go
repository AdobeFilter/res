package routing

import "valhalla/common/api"

const (
	MaxAcceptableRTT  = 500.0  // ms
	MaxBandwidth      = 1000.0 // Mbps
	MaxConnections    = 10000
)

// Weights for the cost function.
type Weights struct {
	RTT         float64
	Bandwidth   float64
	Load        float64
	Reliability float64
}

// DefaultWeights returns the default cost function weights.
func DefaultWeights() Weights {
	return Weights{
		RTT:         0.40,
		Bandwidth:   0.25,
		Load:        0.15,
		Reliability: 0.20,
	}
}

// Cost calculates the cost of a route hop based on node metrics.
// Lower cost = better route.
//
// C(hop) = w1 * (RTT/MaxRTT) + w2 * (1 - bw/MaxBW) + w3 * (conns/MaxConns) + w4 * packet_loss
func Cost(m *api.Metrics, w Weights) float64 {
	if m == nil {
		return 999.0 // very high cost for nodes without metrics
	}

	rttNorm := clamp(m.RTTMs/MaxAcceptableRTT, 0, 1)
	bwNorm := 1.0 - clamp(m.BandwidthMbps/MaxBandwidth, 0, 1)
	loadNorm := clamp(float64(m.ActiveConns)/MaxConnections, 0, 1)
	lossNorm := clamp(m.PacketLoss, 0, 1)

	return w.RTT*rttNorm + w.Bandwidth*bwNorm + w.Load*loadNorm + w.Reliability*lossNorm
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
