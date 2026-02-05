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

package dsl

// This file contains usage examples for Pipeline DSL Parser
// It demonstrates how to use the parser, validator, and processor

/*
Example 1: Basic DSL Parsing

	dslJSON := `{
		"namespace": "production",
		"version": "1.0.0",
		"variables": {
			"ENV": "prod",
			"REGISTRY": "registry.example.com"
		},
		"jobs": [
			{
				"name": "build",
				"description": "Build application",
				"timeout": "30m",
				"source": {
					"type": "git",
					"repo": "https://github.com/example/app.git",
					"branch": "main"
				},
				"steps": [
					{
						"name": "checkout",
						"uses": "git@1.0.0",
						"action": "clone"
					},
					{
						"name": "build",
						"uses": "docker@1.0.0",
						"action": "build",
						"args": {
							"image": "app:latest"
						}
					}
				],
				"target": {
					"type": "k8s",
					"config": {
						"namespace": "production",
						"deployment": "app"
					}
				}
			}
		]
	}`

	logger := log.Logger{Log: log.GetLogger()}
	parser := NewDSLParser(logger)
	pipeline, err := parser.Parse(dslJSON)
	if err != nil {
		// Handle error
	}

Example 2: Processing DSL with Variable Resolution

	dslConfig := `{
		"namespace": "test",
		"variables": {
			"BRANCH": "main",
			"IMAGE_TAG": "${{ env.BUILD_NUMBER }}"
		},
		"jobs": [
			{
				"name": "deploy",
				"when": "${{ env.BRANCH == 'main' }}",
				"steps": [
					{
						"name": "deploy",
						"uses": "k8s@1.0.0",
						"args": {
							"image": "app:${{ variables.IMAGE_TAG }}"
						}
					}
				]
			}
		]
	}`

	processor := NewDSLProcessor(logger)
	additionalEnv := map[string]string{
		"BUILD_NUMBER": "123",
		"BRANCH":       "main",
	}

	pipeline, execCtx, err := processor.ProcessConfig(
		context.Background(),
		dslConfig,
		pluginManager,
		"/workspace",
		additionalEnv,
	)
	if err != nil {
		// Handle error
	}

	// Now pipeline is ready to execute
	runner := NewPipelineRunner(execCtx)
	err = runner.Run(context.Background(), pipeline)

Example 3: Validation Only

	parser := NewDSLParser(logger)
	basicValidator := NewPipelineBasicValidatorAdapter(parser)
	validator := validation.NewValidator(basicValidator)

	pipeline, err := parser.Parse(dslJSON)
	if err != nil {
		// Handle parse error
	}

	err = validator.Validate(pipeline)
	if err != nil {
		// Handle validation error
	}

Example 4: Complex Pipeline with Multiple Jobs

	dslJSON := `{
		"namespace": "ci",
		"version": "2.0.0",
		"variables": {
			"GO_VERSION": "1.21",
			"TEST_TIMEOUT": "10m"
		},
		"runtime": {
			"type": "docker",
			"image": "golang:1.21",
			"resources": {
				"cpu_request": "1",
				"cpu_limit": "2",
				"memory_request": "2Gi",
				"memory_limit": "4Gi"
			}
		},
		"jobs": [
			{
				"name": "test",
				"concurrency": "test-group",
				"timeout": "${{ variables.TEST_TIMEOUT }}",
				"steps": [
					{
						"name": "unit-test",
						"uses": "shell@1.0.0",
						"action": "run",
						"args": {
							"command": "go test ./..."
						}
					},
					{
						"name": "integration-test",
						"uses": "shell@1.0.0",
						"action": "run",
						"args": {
							"command": "go test -tags=integration ./..."
						},
						"continue_on_error": true
					}
				]
			},
			{
				"name": "build",
				"depends_on": ["test"],
				"steps": [
					{
						"name": "build-binary",
						"uses": "docker@1.0.0",
						"action": "build",
						"args": {
							"dockerfile": "Dockerfile",
							"image": "app:${{ env.BUILD_NUMBER }}"
						}
					}
				],
				"approval": {
					"required": true,
					"type": "manual",
					"plugin": "approval@1.0.0"
				},
				"target": {
					"type": "k8s",
					"config": {
						"namespace": "production",
						"deployment": "app"
					}
				},
				"notify": {
					"on_success": {
						"plugin": "slack@1.0.0",
						"action": "Send",
						"params": {
							"channel": "#deployments",
							"message": "Deployment succeeded"
						}
					},
					"on_failure": {
						"plugin": "slack@1.0.0",
						"action": "Send",
						"params": {
							"channel": "#alerts",
							"message": "Deployment failed"
						}
					}
				}
			}
		]
	}`

Example 5: Using Agent Selector

	dslJSON := `{
		"namespace": "distributed",
		"jobs": [
			{
				"name": "build-linux",
				"steps": [
					{
						"name": "build",
						"uses": "docker@1.0.0",
						"agent_selector": {
							"match_labels": {
								"os": "linux",
								"arch": "amd64"
							}
						},
						"run_on_agent": true
					}
				]
			},
			{
				"name": "build-windows",
				"steps": [
					{
						"name": "build",
						"uses": "docker@1.0.0",
						"agent_selector": {
							"match_expressions": [
								{
									"key": "os",
									"operator": "In",
									"values": ["windows"]
								}
							]
						},
						"run_on_agent": true
					}
				]
			}
		]
	}`

*/
