package workerconfig

import "github.com/mathmach/gpu-grid-worker/pkg/domain/nodecore"

func NormalizeGatewayURL(raw string) (string, error) {
	return nodecore.NormalizeGatewayURL(raw)
}

func IsLocalOrPrivateLANHost(host string) bool {
	return nodecore.IsLocalOrPrivateGatewayHost(host)
}
