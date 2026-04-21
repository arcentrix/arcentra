// Copyright 2026 Arcentra Authors.
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

package engine

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// cancelJobRunOnAgent dials the Agent gRPC server and sends a CancelJobRun request.
func cancelJobRunOnAgent(agentAddr, jobRunID, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial agent %s: %w", agentAddr, err)
	}
	defer conn.Close()

	client := agentv1.NewAgentServiceClient(conn)
	_, err = client.CancelJobRun(ctx, &agentv1.CancelJobRunRequest{
		JobRunId: jobRunID,
		Reason:   reason,
	})
	return err
}
