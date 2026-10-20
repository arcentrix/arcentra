// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tracecontext "github.com/arcentrix/arcentra/pkg/trace/context"
	"github.com/gofiber/fiber/v2"
)

// TestRouterInstallsOwnedContextBeforeTracing 验证生产路由在链路追踪前建立可取消请求上下文
func TestRouterInstallsOwnedContextBeforeTracing(t *testing.T) {
	rt := newProbeTestRouter(nil)
	var captured context.Context
	rt.Readiness = func() bool {
		captured = tracecontext.GetContext()
		return captured != nil && captured.Done() != nil && captured.Err() == nil
	}
	app := rt.Router()
	probeResponse(t, app, fiber.MethodGet, "/ready", fiber.StatusOK)
	if captured == nil || captured.Err() != context.Canceled {
		t.Fatal("production middleware chain did not cancel the traced request context")
	}
}

// RequestCtx implements context.Context, so the compiler cannot reject this
// unsafe lifetime escape. Keep native access narrowly allowlisted for parsing.
// TestHTTPContextsDoNotEscapePooledRequestCtx 禁止生产请求上下文逃逸到下游异步读取者
func TestHTTPContextsDoNotEscapePooledRequestCtx(t *testing.T) {
	roots := []string{".", "../../../pkg/http/middleware", "../../../pkg/trace/inject"}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, entry.Name())
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			parents := make(map[ast.Node]ast.Node)
			var stack []ast.Node
			ast.Inspect(file, func(node ast.Node) bool {
				if node == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				if len(stack) > 0 {
					parents[node] = stack[len(stack)-1]
				}
				stack = append(stack, node)
				return true
			})
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) != 0 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Context" {
					return true
				}
				if parent, ok := parents[call].(*ast.SelectorExpr); ok && parent.X == call && parent.Sel.Name == "QueryArgs" && root == "." && entry.Name() == "router_inbox.go" {
					return true
				}
				t.Errorf("%s: pooled RequestCtx must not escape into a context.Context; use UserContext", fset.Position(call.Pos()))
				return true
			})
		}
	}
}
