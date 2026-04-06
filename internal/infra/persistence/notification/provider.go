// Copyright 2025 Arcentra Authors.
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

package notification

import (
	domain "github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/google/wire"
)

// ProviderSet provides all notification persistence bindings via Wire.
var ProviderSet = wire.NewSet(
	NewNotificationChannelRepo,
	wire.Bind(new(domain.INotificationChannelRepo), new(*NotificationChannelRepo)),
	NewNotificationTemplateRepo,
	wire.Bind(new(domain.INotificationTemplateRepo), new(*NotificationTemplateRepo)),
	NewTemplateRepo,
	wire.Bind(new(domain.ITemplateRepository), new(*TemplateRepo)),
	NewChannelRepo,
	wire.Bind(new(domain.ChannelRepository), new(*ChannelRepo)),
)
