// Copyright 2025 Arcentra Team
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

package executor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/go-resty/resty/v2"
)

// HTTPExecutor HTTP 执行器
// 执行 HTTP 类型的请求
// 注意：HTTPExecutor 不应直接注册到 ExecutorManager，而是通过 PluginExecutor 内部调用
// PluginExecutor 会根据 step args 中是否包含 url 字段来判断是否使用 HTTP 执行
type HTTPExecutor struct {
	client *resty.Client
	logger log.Logger
}

// NewHTTPExecutor 创建 HTTP 执行器
func NewHTTPExecutor(logger log.Logger) *HTTPExecutor {
	client := resty.New()
	client.SetTimeout(30 * time.Second) // 默认超时 30 秒
	client.SetRedirectPolicy(resty.FlexibleRedirectPolicy(15))

	return &HTTPExecutor{
		client: client,
		logger: logger,
	}
}

// Name 返回执行器名称
func (e *HTTPExecutor) Name() string {
	return "http"
}

// CanExecute 检查是否可以执行
// HTTP 执行器可以执行 ExecutionType 为 HTTP 的 plugin
func (e *HTTPExecutor) CanExecute(req *ExecutionRequest) bool {
	if req == nil || req.Step == nil {
		return false
	}
	// 需要检查 plugin 的 ExecutionType
	// 这里暂时返回 false，因为需要从 plugin manager 获取 plugin info
	// 实际使用中应该通过 PluginExecutor 来统一处理
	return false
}

// Execute 执行 HTTP 请求
func (e *HTTPExecutor) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	result := NewExecutionResult(e.Name())

	if req.Step == nil {
		err := fmt.Errorf("step is nil")
		result.Complete(false, -1, err)
		return result, err
	}

	// 从 step args 中提取 HTTP 配置
	httpConfig, err := e.extractHTTPConfig(req.Step.Args)
	if err != nil {
		err = fmt.Errorf("extract HTTP config: %w", err)
		result.Complete(false, -1, err)
		return result, err
	}

	// 设置超时 context
	requestCtx := ctx
	if httpConfig.Timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, time.Duration(httpConfig.Timeout)*time.Second)
		defer cancel()
	}

	// 选择 client（根据重定向策略）
	// 注意：resty v2 的重定向策略在 client 级别设置
	client := e.client
	if !httpConfig.FollowRedirects {
		// 创建一个不跟随重定向的临时 client
		tempClient := resty.New()
		if httpConfig.Timeout > 0 {
			tempClient.SetTimeout(time.Duration(httpConfig.Timeout) * time.Second)
		} else {
			tempClient.SetTimeout(30 * time.Second)
		}
		tempClient.SetRedirectPolicy(resty.NoRedirectPolicy())
		client = tempClient
	}

	// 创建请求
	restyReq := client.R().SetContext(requestCtx)

	// 设置请求方法
	method := strings.ToUpper(httpConfig.Method)
	if method == "" {
		method = "GET"
	}

	// 设置 URL
	url := httpConfig.URL
	if url == "" {
		err := fmt.Errorf("URL is required for HTTP execution")
		result.Complete(false, -1, err)
		return result, err
	}

	// 设置请求头
	if len(httpConfig.Headers) > 0 {
		restyReq.SetHeaders(httpConfig.Headers)
	}

	// 设置查询参数
	if len(httpConfig.Query) > 0 {
		restyReq.SetQueryParams(httpConfig.Query)
	}

	// 设置请求体（对于 POST, PUT, PATCH）
	if httpConfig.Body != "" && (method == "POST" || method == "PUT" || method == "PATCH") {
		restyReq.SetBody(httpConfig.Body)
	}

	// 设置环境变量（通过请求头传递）
	if len(req.Env) > 0 {
		for k, v := range req.Env {
			restyReq.SetHeader(fmt.Sprintf("X-Env-%s", k), v)
		}
	}

	// 执行请求
	var resp *resty.Response
	var httpErr error

	startTime := time.Now()
	switch method {
	case "GET":
		resp, httpErr = restyReq.Get(url)
	case "POST":
		resp, httpErr = restyReq.Post(url)
	case "PUT":
		resp, httpErr = restyReq.Put(url)
	case "PATCH":
		resp, httpErr = restyReq.Patch(url)
	case "DELETE":
		resp, httpErr = restyReq.Delete(url)
	case "HEAD":
		resp, httpErr = restyReq.Head(url)
	case "OPTIONS":
		resp, httpErr = restyReq.Options(url)
	default:
		err := fmt.Errorf("unsupported HTTP method: %s", method)
		result.Complete(false, -1, err)
		return result, err
	}
	duration := time.Since(startTime)

	// 处理响应
	if httpErr != nil {
		err = fmt.Errorf("HTTP request failed: %w", httpErr)
		result.Complete(false, -1, err)
		return result, err
	}

	// 检查状态码
	statusCode := resp.StatusCode()
	result.ExitCode = int32(statusCode)

	// 检查是否在期望的状态码范围内
	expectedStatus := httpConfig.ExpectedStatus
	if len(expectedStatus) == 0 {
		// 默认期望 2xx
		expectedStatus = []int32{200, 201, 202, 204}
	}

	isSuccess := false
	for _, code := range expectedStatus {
		if statusCode == int(code) {
			isSuccess = true
			break
		}
	}

	// 如果没有匹配的期望状态码，检查是否是 2xx
	if !isSuccess && statusCode >= 200 && statusCode < 300 {
		isSuccess = true
	}

	result.Success = isSuccess
	result.Output = string(resp.Body())
	result.Duration = duration

	// 构建响应结果
	responseData := map[string]any{
		"status_code": statusCode,
		"headers":     resp.Header(),
		"body":        string(resp.Body()),
		"duration_ms": duration.Milliseconds(),
		"success":     isSuccess,
	}

	responseJSON, _ := sonic.Marshal(responseData)
	result.Output = string(responseJSON)

	if !isSuccess {
		result.Error = fmt.Sprintf("HTTP request returned status code %d, expected one of %v", statusCode, expectedStatus)
	}

	result.Complete(result.Success, result.ExitCode, nil)

	if e.logger.Log != nil {
		e.logger.Log.Debugw("HTTP execution completed",
			"step", req.Step.Name,
			"url", url,
			"method", method,
			"status_code", statusCode,
			"success", result.Success,
			"duration", duration)
	}

	return result, nil
}

// HTTPConfig HTTP 配置
type HTTPConfig struct {
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	Query           map[string]string `json:"query"`
	ExpectedStatus  []int32           `json:"expected_status"`
	FollowRedirects bool              `json:"follow_redirects"`
	Timeout         int32             `json:"timeout"`
}

// extractHTTPConfig 从 step args 中提取 HTTP 配置
func (e *HTTPExecutor) extractHTTPConfig(args map[string]any) (*HTTPConfig, error) {
	config := &HTTPConfig{
		Method:          "GET",
		FollowRedirects: true,
		Timeout:         30,
		Headers:         make(map[string]string),
		Query:           make(map[string]string),
		ExpectedStatus:  []int32{200, 201, 202, 204},
	}

	if args == nil {
		return config, nil
	}

	// 提取 method
	if method, ok := args["method"].(string); ok {
		config.Method = method
	}

	// 提取 URL
	if url, ok := args["url"].(string); ok {
		config.URL = url
	}

	// 提取 headers
	if headers, ok := args["headers"].(map[string]any); ok {
		for k, v := range headers {
			if vStr, ok := v.(string); ok {
				config.Headers[k] = vStr
			}
		}
	}

	// 提取 body
	if body, ok := args["body"].(string); ok {
		config.Body = body
	}

	// 提取 query
	if query, ok := args["query"].(map[string]any); ok {
		for k, v := range query {
			if vStr, ok := v.(string); ok {
				config.Query[k] = vStr
			}
		}
	}

	// 提取 expected_status
	if expectedStatus, ok := args["expected_status"].([]any); ok {
		config.ExpectedStatus = make([]int32, 0, len(expectedStatus))
		for _, v := range expectedStatus {
			switch val := v.(type) {
			case int32:
				config.ExpectedStatus = append(config.ExpectedStatus, val)
			case int:
				config.ExpectedStatus = append(config.ExpectedStatus, int32(val))
			case float64:
				config.ExpectedStatus = append(config.ExpectedStatus, int32(val))
			case string:
				if code, err := strconv.ParseInt(val, 10, 32); err == nil {
					config.ExpectedStatus = append(config.ExpectedStatus, int32(code))
				}
			}
		}
	}

	// 提取 follow_redirects
	if followRedirects, ok := args["follow_redirects"].(bool); ok {
		config.FollowRedirects = followRedirects
	}

	// 提取 timeout
	if timeout, ok := args["timeout"].(float64); ok {
		config.Timeout = int32(timeout)
	} else if timeout, ok := args["timeout"].(int); ok {
		config.Timeout = int32(timeout)
	}

	return config, nil
}
