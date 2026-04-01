package metrics

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valhalla/common/api"
)

// Collect gathers current node metrics.
func Collect() api.Metrics {
	return api.Metrics{
		RTTMs:         measureRTT(),
		BandwidthMbps: 0, // TODO: implement bandwidth measurement
		CPUPercent:    getCPUPercent(),
		ActiveConns:   getActiveConnections(),
		PacketLoss:    0, // TODO: implement packet loss measurement
		RecordedAt:    time.Now(),
	}
}

func measureRTT() float64 {
	start := time.Now()
	cmd := exec.Command("ping", "-c", "1", "-W", "2", "8.8.8.8")
	if err := cmd.Run(); err != nil {
		return 999.0
	}
	return float64(time.Since(start).Milliseconds())
}

func getCPUPercent() float64 {
	// Simple CPU measurement using /proc/stat on Linux
	if runtime.GOOS != "linux" {
		return 0
	}

	cmd := exec.Command("bash", "-c",
		`grep 'cpu ' /proc/stat | awk '{usage=($2+$4)*100/($2+$4+$5)} END {print usage}'`)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return val
}

func getActiveConnections() int {
	cmd := exec.Command("bash", "-c", "ss -tun | wc -l")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	val, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return val - 1 // subtract header
}
