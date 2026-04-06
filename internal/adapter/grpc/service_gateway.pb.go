package grpc

import (
	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
)

type GatewayServiceImpl struct {
	gatewayv1.UnimplementedGatewayServiceServer
}

func NewGatewayServiceImpl() *GatewayServiceImpl {
	return &GatewayServiceImpl{}
}
