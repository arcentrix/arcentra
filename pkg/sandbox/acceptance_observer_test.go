//go:build sandbox_acceptance

package sandbox

import (
	"context"
	"errors"
	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/namespaces"
	"google.golang.org/grpc"
	"testing"
)

func TestAcceptanceObserverOnlyRecordsExactLifecycleWithoutChangingRPC(t *testing.T) {
	t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", "1")
	t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "1")
	namespace := "arcentra-containerjob-acceptance-observer-unit"
	recorded := map[string]int{}
	remove, err := ObserveAcceptanceLifecycle(namespace, func(method, id string) {
		if id != "owned-resource" {
			t.Fatal("unexpected recorded identity")
		}
		recorded[method]++
	})
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if _, err := ObserveAcceptanceLifecycle(namespace, func(string, string) {}); err == nil {
		t.Fatal("duplicate observer installed")
	}
	sentinel := errors.New("original transport error")
	for _, test := range []struct {
		method  string
		request any
		want    int
	}{
		{"/containerd.services.containers.v1.Containers/Create", &containersapi.CreateContainerRequest{Container: &containersapi.Container{ID: "owned-resource"}}, 1},
		{"/containerd.services.tasks.v1.Tasks/Create", &tasksapi.CreateTaskRequest{ContainerID: "owned-resource"}, 1},
		{"/containerd.services.tasks.v1.Tasks/Start", &tasksapi.StartRequest{ContainerID: "owned-resource"}, 1},
		{"/containerd.services.tasks.v1.Tasks/Start", &tasksapi.StartRequest{ContainerID: "owned-resource", ExecID: "command"}, 1},
	} {
		response := &struct{}{}
		invoked := 0
		err := observeAcceptanceRPC(namespaces.WithNamespace(context.Background(), namespace), test.method, test.request, response, nil, func(_ context.Context, method string, request, reply any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			invoked++
			if method != test.method || request != test.request || reply != response {
				t.Fatal("observer changed RPC")
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) || invoked != 1 || recorded[test.method] != test.want {
			t.Fatalf("incorrect pass-through/audit: %v %d %v", err, invoked, recorded)
		}
	}
	before := len(recorded)
	if err := observeAcceptanceRPC(namespaces.WithNamespace(context.Background(), "another-namespace"), "/containerd.services.tasks.v1.Tasks/Start", &tasksapi.StartRequest{ContainerID: "foreign"}, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }); err != nil || len(recorded) != before {
		t.Fatal("foreign namespace observed")
	}
	remove()
	remove()
}
