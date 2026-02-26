// Copyright 2026 Arcentra Authors.
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

package outbox

import "errors"

var (
	// ErrAgentIdRequired is returned when AgentId is empty.
	ErrAgentIdRequired = errors.New("outbox: agent_id is required")
	// ErrScopeTooLong is returned when scope id exceeds max length.
	ErrScopeTooLong = errors.New("outbox: scope id exceeds max length")
	// ErrClosed is returned when operating on a closed outbox.
	ErrClosed = errors.New("outbox: closed")
	// ErrDiskFull is returned when disk usage exceeds limit.
	ErrDiskFull = errors.New("outbox: disk usage exceeds limit")
)
