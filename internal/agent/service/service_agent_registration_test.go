package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// 模拟控制面现有路由：注册令牌优先，每次动态注册创建不同身份
type restartRegistrationClient struct {
	agentv1.AgentServiceClient
	requests    []*agentv1.RegisterRequest
	enrollments int
}

func (c *restartRegistrationClient) Register(_ context.Context, req *agentv1.RegisterRequest, _ ...grpc.CallOption) (*agentv1.RegisterResponse, error) {
	c.requests = append(c.requests, proto.Clone(req).(*agentv1.RegisterRequest))
	if req.RegistrationToken != "" {
		c.enrollments++
		id := fmt.Sprintf("synthetic-agent-%d", c.enrollments)
		return &agentv1.RegisterResponse{Success: true, AgentId: id, AuthToken: id + ":synthetic-signature"}, nil
	}
	if req.AgentId == "synthetic-agent-1" && req.Token == "synthetic-agent-1:synthetic-signature" {
		return &agentv1.RegisterResponse{Success: true, AgentId: req.AgentId}, nil
	}
	return nil, status.Error(codes.Unauthenticated, "invalid per-agent token")
}

type registrationJournalSnapshot struct {
	Data string
	Mode os.FileMode
}

func snapshotRegistrationJournal(t *testing.T, directory string) map[string]registrationJournalSnapshot {
	t.Helper()
	result := make(map[string]registrationJournalSnapshot)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[path] = registrationJournalSnapshot{Data: string(data), Mode: info.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func firstRegistrationLifecycle(t *testing.T, client *restartRegistrationClient) (*config.AgentConfig, string) {
	t.Helper()
	conf := &config.AgentConfig{
		Agent: config.AgentInfo{WorkspaceDir: t.TempDir()},
		Grpc:  config.GrpcConfig{ServerAddr: "controller:9090", RegistrationToken: "art_synthetic_enrollment"},
	}
	svc := NewAgentServiceImpl(conf, &agentgrpc.ClientWrapper{AgentClient: client}, nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if conf.Agent.ID != "synthetic-agent-1" || conf.Grpc.Token != "synthetic-agent-1:synthetic-signature" || client.enrollments != 1 {
		t.Fatal("first lifecycle did not dynamically enroll the synthetic agent")
	}
	directory := filepath.Join(conf.Agent.WorkspaceDir, ".terminal-report-outbox")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256([]byte(conf.Agent.ID + "\x00" + conf.Grpc.ServerAddr))
	if err := os.WriteFile(filepath.Join(directory, "identity"), fmt.Appendf(nil, "%x\n", binding), 0o640); err != nil {
		t.Fatal(err)
	}
	// 注册检查不能尝试恢复、清理或重写未完成的日志文件
	if err := os.WriteFile(filepath.Join(directory, ".pending"), []byte("synthetic pending terminal write"), 0o640); err != nil {
		t.Fatal(err)
	}
	return conf, directory
}

func TestRegistrationRestartPreservesConfiguredIdentity(t *testing.T) {
	client := &restartRegistrationClient{}
	first, directory := firstRegistrationLifecycle(t, client)
	before := snapshotRegistrationJournal(t, directory)
	// 新实例只使用操作员显式配置的原始身份，不能依赖上一进程的内存
	restarted := &config.AgentConfig{
		Agent: config.AgentInfo{ID: first.Agent.ID, WorkspaceDir: first.Agent.WorkspaceDir},
		Grpc:  config.GrpcConfig{ServerAddr: first.Grpc.ServerAddr, Token: first.Grpc.Token, RegistrationToken: first.Grpc.RegistrationToken},
	}
	svc := NewAgentServiceImpl(restarted, &agentgrpc.ClientWrapper{AgentClient: client}, nil)
	resp, err := svc.Register(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.AgentId != first.Agent.ID || restarted.Agent.ID != first.Agent.ID || client.enrollments != 1 {
		t.Fatalf("restart replaced the original identity: first=%q restarted=%q enrollments=%d", first.Agent.ID, restarted.Agent.ID, client.enrollments)
	}
	if len(client.requests) != 2 || client.requests[1].RegistrationToken != "" || client.requests[1].Token != first.Grpc.Token {
		t.Fatal("configured credentials did not take precedence over the enrollment token")
	}
	if after := snapshotRegistrationJournal(t, directory); !reflect.DeepEqual(before, after) {
		t.Fatal("registration changed existing journal contents or permissions")
	}
}

func TestRegistrationRestartWithoutCredentialsFailsBeforeEnrollment(t *testing.T) {
	for _, partial := range []string{"none", "id-only", "token-only", "blank-id", "blank-token"} {
		t.Run(partial, func(t *testing.T) {
			client := &restartRegistrationClient{}
			first, directory := firstRegistrationLifecycle(t, client)
			before := snapshotRegistrationJournal(t, directory)
			restarted := &config.AgentConfig{
				Agent: config.AgentInfo{WorkspaceDir: first.Agent.WorkspaceDir},
				Grpc:  config.GrpcConfig{ServerAddr: first.Grpc.ServerAddr, RegistrationToken: first.Grpc.RegistrationToken},
			}
			switch partial {
			case "id-only":
				restarted.Agent.ID = first.Agent.ID
			case "token-only":
				restarted.Grpc.Token = first.Grpc.Token
			case "blank-id":
				restarted.Agent.ID, restarted.Grpc.Token = " ", first.Grpc.Token
			case "blank-token":
				restarted.Agent.ID, restarted.Grpc.Token = first.Agent.ID, " "
			}
			configured := *restarted
			svc := NewAgentServiceImpl(restarted, &agentgrpc.ClientWrapper{AgentClient: client}, nil)
			_, err := svc.Register(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "original agent.id and grpc.token") {
				t.Fatalf("missing actionable restart identity error: %v", err)
			}
			if len(client.requests) != 1 || client.enrollments != 1 {
				t.Fatal("restart contacted the controller and enrolled a replacement agent")
			}
			if !reflect.DeepEqual(configured, *restarted) {
				t.Fatal("failed restart changed configured identity")
			}
			if after := snapshotRegistrationJournal(t, directory); !reflect.DeepEqual(before, after) {
				t.Fatal("failed restart changed existing journal contents or permissions")
			}
		})
	}
}

func TestRegistrationConfiguredCredentialsDoNotFallbackOnFailure(t *testing.T) {
	client := &restartRegistrationClient{}
	conf := &config.AgentConfig{
		Agent: config.AgentInfo{ID: "synthetic-agent-1", WorkspaceDir: t.TempDir()},
		Grpc:  config.GrpcConfig{Token: "synthetic-agent-1:invalid", RegistrationToken: "art_synthetic_enrollment"},
	}
	configured := *conf
	svc := NewAgentServiceImpl(conf, &agentgrpc.ClientWrapper{AgentClient: client}, nil)
	_, err := svc.Register(context.Background(), nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid per-agent credentials did not fail closed: %v", err)
	}
	if len(client.requests) != 1 || client.requests[0].RegistrationToken != "" || client.enrollments != 0 {
		t.Fatal("failed legacy authentication fell back to dynamic enrollment")
	}
	if !reflect.DeepEqual(configured, *conf) {
		t.Fatal("failed authentication changed configured identity")
	}
}
