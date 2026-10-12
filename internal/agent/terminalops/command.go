// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

// Package terminalops 提供不初始化 Agent 或网络客户端的本地终态隔离审查命令
package terminalops

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/arcentrix/arcentra/internal/agent/taskqueue"
	"github.com/bytedance/sonic"
)

const (
	commandName    = "terminal-quarantine"
	maxOutputBytes = 8 << 20
	maxListLimit   = 1000
	maxListOffset  = 10000
)

const usage = `Usage:
  arcentra-agent terminal-quarantine list --directory PATH --agent-id ID --controller ADDRESS [--limit 100] [--offset 0]
  arcentra-agent terminal-quarantine inspect --directory PATH --agent-id ID --controller ADDRESS --kind job|step --run-id ID
  arcentra-agent terminal-quarantine mark-reviewed --directory PATH --agent-id ID --controller ADDRESS --kind job|step --run-id ID --disposition investigated|verified_external_effects --confirm-reviewed

Stop the Agent first. These commands use its existing exclusive journal lock.
All identity selectors are required; no configuration, credentials, or network is used.
The directory is the existing .terminal-report-outbox, not its workspace parent.
Review records only a self-reported local operator action. It does not accept,
retry, delete, replay, or change a terminal result or authorize another execution.
`

type quarantineStore interface {
	List(context.Context) ([]taskqueue.TerminalQuarantineSummary, error)
	Inspect(context.Context, string, string) (taskqueue.TerminalQuarantineSummary, error)
	MarkReviewed(context.Context, string, string, string) (taskqueue.TerminalQuarantineSummary, error)
	Close() error
}

type openStore func(context.Context, string, string, string) (quarantineStore, error)

type options struct {
	action, directory, agentID, controller, kind, runID, disposition string
	confirmed                                                        bool
	limit, offset                                                    int
}

type listResult struct {
	Records    []taskqueue.TerminalQuarantineSummary `json:"records"`
	Total      int                                   `json:"total"`
	Offset     int                                   `json:"offset"`
	NextOffset *int                                  `json:"nextOffset,omitempty"`
}

// Run 在任何配置加载或 Agent 引导前分派离线命令，返回是否处理以及进程退出码
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) (handled bool, exitCode int) {
	return run(ctx, args, stdout, stderr, func(ctx context.Context, directory, agentID, controller string) (quarantineStore, error) {
		return taskqueue.OpenTerminalQuarantine(ctx, directory, agentID, controller)
	})
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, open openStore) (bool, int) {
	if len(args) == 0 || args[0] != commandName {
		return false, 0
	}
	opts, help, err := parse(args[1:])
	if help {
		if _, err := io.WriteString(stdout, usage); err != nil {
			writeError(stderr, "could not write command help")
			return true, 1
		}
		return true, 0
	}
	if err != nil {
		writeError(stderr, err.Error())
		_, _ = io.WriteString(stderr, usage)
		return true, 2
	}
	// 本地文件操作和输出均有固定规模；超时或信号取消后不继续审查写入
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		writeError(stderr, "command canceled")
		return true, 1
	}
	store, err := open(ctx, opts.directory, opts.agentID, opts.controller)
	if err != nil {
		writeError(stderr, "could not open existing outbox; stop the Agent and verify directory, identity, ownership, and journal integrity")
		return true, 1
	}
	result, operationErr := execute(ctx, store, opts)
	closeErr := store.Close()
	if operationErr != nil {
		writeError(stderr, operationErr.Error())
		return true, 1
	}
	if closeErr != nil {
		writeError(stderr, "could not close outbox; verify journal state before continuing")
		return true, 1
	}
	if ctx.Err() != nil {
		writeError(stderr, "command canceled; inspect the selected record before repeating a review")
		return true, 1
	}
	if n, err := stdout.Write(result); err != nil || n != len(result) {
		writeError(stderr, "could not write result; inspect the selected record before repeating a review")
		return true, 1
	}
	return true, 0
}

func parse(args []string) (options, bool, error) {
	var opts options
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		return opts, true, nil
	}
	if len(args) == 0 {
		return opts, false, errors.New("a quarantine subcommand is required")
	}
	opts.action = args[0]
	if opts.action != "list" && opts.action != "inspect" && opts.action != "mark-reviewed" {
		return opts, false, errors.New("unknown quarantine subcommand")
	}
	flags := flag.NewFlagSet(commandName, flag.ContinueOnError)
	// flag 包错误可能回显输入的路径、标识或错误粘贴的凭据，统一用固定错误替代
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.directory, "directory", "", "existing terminal outbox directory")
	flags.StringVar(&opts.agentID, "agent-id", "", "original Agent ID")
	flags.StringVar(&opts.controller, "controller", "", "original normalized controller logical address")
	if opts.action == "list" {
		flags.IntVar(&opts.limit, "limit", 100, "number of summaries, at most 1000")
		flags.IntVar(&opts.offset, "offset", 0, "sorted result offset")
	} else {
		flags.StringVar(&opts.kind, "kind", "", "job or step")
		flags.StringVar(&opts.runID, "run-id", "", "quarantined run ID")
	}
	if opts.action == "mark-reviewed" {
		flags.StringVar(&opts.disposition, "disposition", "", "investigated or verified_external_effects")
		flags.BoolVar(&opts.confirmed, "confirm-reviewed", false, "confirm a self-reported local review")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, true, nil
		}
		return opts, false, errors.New("invalid quarantine command arguments")
	}
	if flags.NArg() != 0 {
		return opts, false, errors.New("unexpected positional arguments")
	}
	if !validSelector(opts.directory, 4096) || !validSelector(opts.agentID, 64) || !validSelector(opts.controller, 4096) {
		return opts, false, errors.New("explicit directory, agent-id, and controller selectors are required and must be valid")
	}
	if opts.action == "list" {
		if opts.limit < 1 || opts.limit > maxListLimit || opts.offset < 0 || opts.offset > maxListOffset {
			return opts, false, errors.New("list limit must be 1..1000 and offset must be 0..10000")
		}
		return opts, false, nil
	}
	if (opts.kind != "job" && opts.kind != "step") || !validSelector(opts.runID, 512) {
		return opts, false, errors.New("explicit kind (job or step) and run-id selectors are required")
	}
	if opts.action == "mark-reviewed" {
		if opts.disposition != "investigated" && opts.disposition != "verified_external_effects" {
			return opts, false, errors.New("disposition must be investigated or verified_external_effects")
		}
		if !opts.confirmed {
			return opts, false, errors.New("mark-reviewed requires --confirm-reviewed to record your local review")
		}
	}
	return opts, false, nil
}

func validSelector(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func execute(ctx context.Context, store quarantineStore, opts options) ([]byte, error) {
	var value any
	switch opts.action {
	case "list":
		records, err := store.List(ctx)
		if err != nil {
			return nil, errors.New("could not list quarantine summaries")
		}
		if len(records) > maxListOffset {
			return nil, errors.New("quarantine summary count exceeds the supported bound")
		}
		start := min(opts.offset, len(records))
		end := min(start+opts.limit, len(records))
		page := listResult{Records: records[start:end], Total: len(records), Offset: opts.offset}
		if page.Records == nil {
			page.Records = []taskqueue.TerminalQuarantineSummary{}
		}
		if end < len(records) {
			page.NextOffset = &end
		}
		value = page
	case "inspect":
		summary, err := store.Inspect(ctx, opts.kind, opts.runID)
		if err != nil {
			return nil, errors.New("could not inspect the selected quarantined record")
		}
		value = summary
	case "mark-reviewed":
		summary, err := store.MarkReviewed(ctx, opts.kind, opts.runID, opts.disposition)
		if err != nil {
			return nil, errors.New("could not mark the selected quarantined record reviewed; an existing disposition is immutable")
		}
		value = summary
	default:
		return nil, errors.New("unknown quarantine subcommand")
	}
	if ctx.Err() != nil {
		return nil, errors.New("command canceled; inspect the selected record before repeating a review")
	}
	data, err := sonic.Marshal(value)
	if err != nil || len(data) >= maxOutputBytes {
		return nil, errors.New("could not encode bounded quarantine summary output")
	}
	return append(data, '\n'), nil
}

func writeError(stderr io.Writer, message string) {
	_, _ = io.WriteString(stderr, "error: "+message+"\n")
}
