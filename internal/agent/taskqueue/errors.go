package taskqueue

import (
	"sort"
	"strings"

	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// redactJobError 保护未经过步骤执行器的准备与条件错误，同时保留取消状态判断
func redactJobError(payload *taskqueue.JobRunTaskPayload, err error) error {
	if err == nil || payload == nil {
		return err
	}
	var secrets []string
	if payload.Source != nil {
		secrets = append(secrets, payload.Source.Auth["password"], payload.Source.Auth["token"])
	}
	for key, value := range payload.Env {
		if strings.HasPrefix(key, "secrets.") {
			secrets = append(secrets, value)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	pairs := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		if secret != "" {
			pairs = append(pairs, secret, "[REDACTED]")
		}
	}
	message := strings.NewReplacer(pairs...).Replace(err.Error())
	return &safeJobError{cause: err, message: message}
}

type safeJobError struct {
	cause   error
	message string
}

func (e *safeJobError) Error() string { return e.message }
func (e *safeJobError) Unwrap() error { return e.cause }
