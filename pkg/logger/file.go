package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// getFileLogWriter creates a rotating file writer.
func getFileLogWriter(config *Conf) (io.Writer, error) {
	if err := os.MkdirAll(config.Path, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	logPath := filepath.Join(config.Path, config.Filename)
	return &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    config.RotateSize,
		MaxBackups: config.RotateNum,
		MaxAge:     config.KeepHours,
		Compress:   true,
	}, nil
}
