// Copyright 2025 Arcentra Team
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpc

import (
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/internal/engine/service"
	"github.com/arcentrix/arcentra/internal/pkg/grpc/interceptor"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ProviderSet 提供 gRPC 服务层相关的依赖（主程序使用）
var ProviderSet = wire.NewSet(
	ProvideGrpcServer,
	ProvideGrpcClient,
)

// ProvideGrpcServer 提供 gRPC 服务器实例
func ProvideGrpcServer(cfg *Conf, services *service.Services, repos *repo.Repositories, cache cache.ICache, mysqlDB *gorm.DB, kafkaSettings service.KafkaSettings) *ServerWrapper {
	server := NewGrpcServer(*cfg)

	// Set up token verifier for agent authentication
	tokenVerifier := interceptor.NewAgentTokenVerifier(services.Agent, repos.Agent, services.GeneralSettings, cache)
	interceptor.SetTokenVerifier(tokenVerifier)

	// 获取 Redis 客户端
	var redisClient *redis.Client
	// 使用类型断言获取 RedisCache，然后调用 GetClient()
	if rc, ok := cache.(interface{ GetClient() *redis.Client }); ok {
		redisClient = rc.GetClient()
	}

	server.Register(services, redisClient, mysqlDB, kafkaSettings)
	return server
}

// ProvideGrpcClient 提供 gRPC 客户端实例
func ProvideGrpcClient(cfg *ClientConf) (*ClientWrapper, error) {
	return NewGrpcClient(*cfg)
}
