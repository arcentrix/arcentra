package pipeline

import (
	"fmt"

	"github.com/bytedance/sonic"
)

func checkPluginResult(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var result map[string]any
	if err := sonic.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode plugin result: %w", err)
	}
	success, explicit := result["success"].(bool)
	exitCode, _ := result["exitCode"].(float64)
	if code, ok := result["exit_code"].(float64); ok {
		exitCode = code
	}
	if explicit && !success || exitCode != 0 {
		message, _ := result["error"].(string)
		if message == "" {
			message = fmt.Sprintf("plugin reported failure (exit code %g)", exitCode)
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}
