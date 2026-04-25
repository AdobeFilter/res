module valhalla/client

go 1.22

require (
	go.uber.org/zap v1.27.0
	golang.org/x/crypto v0.21.0
	golang.zx2c4.com/wireguard v0.0.0-20231211153847-12269c276173
	gvisor.dev/gvisor v0.0.0-20230504175454-7b0a1988a28f
	valhalla/common v0.0.0
)

replace valhalla/common => ../valhalla-common
