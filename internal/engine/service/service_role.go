// Copyright 2025 Arcade Team
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

package service

import (
	"time"

	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/internal/engine/repo"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/util"
)

type RoleService struct {
	roleRepo repo.IRoleRepository
}

func NewRoleService(roleRepo repo.IRoleRepository) *RoleService {
	return &RoleService{
		roleRepo: roleRepo,
	}
}

func (rs *RoleService) CreateRole(createReq *model.CreateRoleReq) (*model.Role, error) {
	isEnabled := 1
	if createReq.IsEnabled != nil {
		isEnabled = *createReq.IsEnabled
	}

	role := &model.Role{
		RoleId:      createReq.RoleId,
		Name:        createReq.Name,
		DisplayName: createReq.DisplayName,
		Description: createReq.Description,
		IsEnabled:   isEnabled,
	}

	if err := rs.roleRepo.CreateRole(role); err != nil {
		log.Errorw("create role failed", "error", err)
		return nil, err
	}

	return role, nil
}

func (rs *RoleService) GetRoleByRoleId(roleId string) (*model.Role, error) {
	role, err := rs.roleRepo.GetRole(roleId)
	if err != nil {
		log.Errorw("get role by roleId failed", "roleId", roleId, "error", err)
		return nil, err
	}
	return role, nil
}

func (rs *RoleService) GetRoleById(id uint64) (*model.Role, error) {
	role, err := rs.roleRepo.GetRoleById(id)
	if err != nil {
		log.Errorw("get role by id failed", "id", id, "error", err)
		return nil, err
	}
	return role, nil
}

func (rs *RoleService) ListRoles(pageNum, pageSize int) ([]model.Role, int64, error) {
	roles, count, err := rs.roleRepo.ListRoles(pageNum, pageSize)
	if err != nil {
		log.Errorw("list roles failed", "error", err)
		return nil, 0, err
	}
	return roles, count, err
}

func (rs *RoleService) UpdateRoleByRoleId(roleId string, updateReq *model.UpdateRoleReq) error {
	// Check if role exists
	_, err := rs.roleRepo.GetRole(roleId)
	if err != nil {
		log.Errorw("get role by roleId failed", "roleId", roleId, "error", err)
		return err
	}

	// Build and update Role fields
	updates := buildRoleUpdateMap(updateReq)
	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := rs.roleRepo.UpdateRoleByRoleId(roleId, updates); err != nil {
			log.Errorw("update role failed", "roleId", roleId, "error", err)
			return err
		}
	}

	return nil
}

func (rs *RoleService) UpdateRoleById(id uint64, updateReq *model.UpdateRoleReq) error {
	// Check if role exists
	_, err := rs.roleRepo.GetRoleById(id)
	if err != nil {
		log.Errorw("get role by id failed", "id", id, "error", err)
		return err
	}

	// Build and update Role fields
	updates := buildRoleUpdateMap(updateReq)
	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := rs.roleRepo.UpdateRoleById(id, updates); err != nil {
			log.Errorw("update role failed", "id", id, "error", err)
			return err
		}
	}

	return nil
}

func (rs *RoleService) DeleteRoleByRoleId(roleId string) error {
	if err := rs.roleRepo.DeleteRoleByRoleId(roleId); err != nil {
		log.Errorw("delete role failed", "roleId", roleId, "error", err)
		return err
	}
	return nil
}

func (rs *RoleService) DeleteRole(id uint64) error {
	if err := rs.roleRepo.DeleteRole(id); err != nil {
		log.Errorw("delete role failed", "id", id, "error", err)
		return err
	}
	return nil
}

// buildRoleUpdateMap builds update map for Role fields
func buildRoleUpdateMap(req *model.UpdateRoleReq) map[string]any {
	updates := make(map[string]any)
	util.SetIfNotNil(updates, "name", req.Name)
	util.SetIfNotNil(updates, "display_name", req.DisplayName)
	util.SetIfNotNil(updates, "description", req.Description)
	util.SetIfNotNil(updates, "is_enabled", req.IsEnabled)
	return updates
}
