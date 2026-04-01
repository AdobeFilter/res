module valhalla/exit-node

go 1.22

require (
	valhalla/common v0.0.0
	github.com/xtls/xray-core v1.8.11
	go.uber.org/zap v1.27.0
	golang.zx2c4.com/wireguard v0.0.0-20231211153847-12269c276173
	golang.zx2c4.com/wireguard/wgctrl v0.0.0-20230429144221-925a1e7659e6
)

require (
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.21.0 // indirect
	golang.org/x/sys v0.18.0 // indirect
)

replace valhalla/common => ../valhalla-common
