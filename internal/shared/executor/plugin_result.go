package executor

import (
	"fmt"
	"github.com/bytedance/sonic"
)

func decodePluginResult(result *ExecutionResult, raw []byte) error {
	result.Success = true
	if len(raw) > 0 {
		var data map[string]any
		if err := sonic.Unmarshal(raw, &data); err != nil {
			result.Complete(false, -1, err)
			return fmt.Errorf("decode plugin result: %w", err)
		}
		result.Output, _ = data["stdout"].(string)
		result.ErrorOutput, _ = data["stderr"].(string)
		result.Error, _ = data["error"].(string)
		if code, ok := data["exit_code"].(float64); ok {
			result.ExitCode = int32(code)
		}
		if code, ok := data["exitCode"].(float64); ok {
			result.ExitCode = int32(code)
		}
		if success, ok := data["success"].(bool); ok {
			result.Success = success
		}
		if result.ExitCode != 0 {
			result.Success = false
		}
		for _, key := range []string{"progress", "artifact"} {
			if value, ok := data[key]; ok {
				result.WithMetadata(key, value)
			}
		}
	}
	var err error
	if !result.Success {
		if result.Error == "" {
			result.Error = fmt.Sprintf("plugin execution failed with exit code %d", result.ExitCode)
		}
		err = fmt.Errorf("%s", result.Error)
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
	}
	result.Complete(result.Success, result.ExitCode, err)
	return err
}
