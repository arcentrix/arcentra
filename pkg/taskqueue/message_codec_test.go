// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/bytedance/sonic"
)

func TestResolveMessageCodec(t *testing.T) {
	for _, tc := range []struct {
		name, format, codec string
		want                nova.MessageFormat
		errField            string
	}{
		{name: "omitted", want: nova.MessageFormatSonic},
		{name: "whitespace", format: " \t", codec: " ", want: nova.MessageFormatSonic},
		{name: "json", format: "json", want: nova.MessageFormatJSON},
		{name: "sonic", format: "sonic", want: nova.MessageFormatSonic},
		{name: "normalized", format: " JSON ", codec: " SoNiC ", want: nova.MessageFormatSonic},
		{name: "explicit json", format: "sonic", codec: "json", want: nova.MessageFormatJSON},
		{name: "codec only", codec: "json", want: nova.MessageFormatJSON},
		{name: "unknown format", format: "typo", codec: "json", errField: "messageFormat"},
		{name: "unknown codec", format: "sonic", codec: "typo", errField: "messageCodec"},
		{name: "protobuf format", format: "protobuf", errField: "messageFormat"},
		{name: "protobuf codec", codec: "protobuf", errField: "messageCodec"},
		{name: "blob format", format: "blob", errField: "messageFormat"},
		{name: "blob codec", codec: "blob", errField: "messageCodec"},
		{name: "shadowed invalid format", format: "protobuf", codec: "sonic", errField: "messageFormat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec, err := ResolveMessageCodec(tc.format, tc.codec)
			if tc.errField != "" {
				if err == nil || codec != nil || !strings.Contains(err.Error(), "taskQueue."+tc.errField) || !strings.Contains(err.Error(), "use json or sonic") {
					t.Fatalf("expected actionable %s rejection, got codec=%v err=%v", tc.errField, codec, err)
				}
				return
			}
			if err != nil || codec.Format() != tc.want {
				t.Fatalf("codec=%v err=%v; want %s", codec, err, tc.want)
			}
		})
	}
}

func TestTaskMessageJSONSonicWireCompatibility(t *testing.T) {
	for _, payload := range []struct {
		taskType string
		value    any
	}{
		{TaskTypeJobRun, JobRunTaskPayload{AgentID: "agent", JobRunID: "job", Steps: []StepPayload{{StepRunID: "step", Args: map[string]any{"run": "printf 'hello世界'"}}}}},
		{TaskTypeStepRun, StepRunTaskPayload{StepRunID: "step"}},
		{TaskTypeJobCancel, JobCancelTaskPayload{AgentID: "agent", JobRunID: "job"}},
	} {
		data, err := sonic.Marshal(payload.value)
		if err != nil {
			t.Fatal(err)
		}
		original := nova.TaskMessage{TaskID: "task-id", Task: &nova.Task{Type: payload.taskType, Payload: data}, Queue: AgentQueue("agent"), Priority: nova.PriorityHigh}
		for _, producerFormat := range []string{"", "json", "sonic"} {
			for _, workerFormat := range []string{"", "json", "sonic"} {
				t.Run(payload.taskType+"/"+producerFormat+"->"+workerFormat, func(t *testing.T) {
					producer, err := ResolveMessageCodec(producerFormat, "")
					if err != nil {
						t.Fatal(err)
					}
					worker, err := ResolveMessageCodec(workerFormat, "")
					if err != nil {
						t.Fatal(err)
					}
					encoded, err := producer.Encode(&original)
					if err != nil {
						t.Fatal(err)
					}
					// 保持既有 Nova 字段名称和字节负载，不重写消息协议
					var fields map[string]any
					if err := sonic.Unmarshal(encoded, &fields); err != nil {
						t.Fatal(err)
					}
					if len(fields) != 4 || fields["task_id"] != original.TaskID || fields["task"] == nil || fields["queue"] != original.Queue || fields["priority"] != float64(original.Priority) {
						t.Fatalf("wire envelope changed: %s", encoded)
					}
					var decoded nova.TaskMessage
					if err := worker.Decode(encoded, &decoded); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(original, decoded) {
						t.Fatalf("wire round trip changed envelope or payload: got %+v", decoded)
					}
				})
			}
		}
	}
}

func TestStartupErrorHidesBrokerSettingsAndPreservesCause(t *testing.T) {
	cause := errors.New("CANARY_PASSWORD in broker config")
	err := WrapStartupError("create control task queue producer", cause)
	for _, rendered := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%+v", err)} {
		if strings.Contains(rendered, "CANARY_PASSWORD") || !strings.Contains(rendered, "check broker settings") {
			t.Fatalf("unsafe startup error: %q", rendered)
		}
	}
	if !errors.Is(err, cause) {
		t.Fatal("underlying initialization cause was lost")
	}
	_, err = ResolveMessageCodec("CANARY_PASSWORD", "sonic")
	if err == nil || strings.Contains(err.Error(), "CANARY_PASSWORD") {
		t.Fatal("codec validation exposed invalid configuration value")
	}
}
