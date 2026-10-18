// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

// liveManualInboxSubject is suitable for historical read checks too: it only
// reconstructs the current exact human/team identity, not execution eligibility.
// Resource locks must already be acquired before this transaction-bound view.
func liveManualInboxSubject(ctx context.Context, view *repo.Repositories, actor string) (authz.Subject, error) {
	denied := repo.ErrManualTriggerForbidden
	subject := authz.Subject{UserID: actor}
	if actor == "" || actor != strings.TrimSpace(actor) || len(actor) > 64 || view == nil || view.User == nil || view.TeamMember == nil || view.Team == nil {
		return subject, denied
	}
	reader, ok := view.User.(repo.IInboxActorReader)
	if !ok {
		return subject, denied
	}
	user, err := reader.GetInboxActor(ctx, actor)
	if err != nil {
		return subject, err
	}
	if user == nil || user.UserID != actor || user.IsEnabled != 1 {
		return subject, denied
	}
	membershipReader, ok := view.TeamMember.(repo.IInboxMembershipReader)
	if !ok {
		return subject, denied
	}
	members, err := membershipReader.GetInboxMemberships(ctx, actor)
	if err != nil {
		return subject, err
	}
	if len(members) > 1000 {
		return subject, denied
	}
	ids := []string{}
	requested := map[string]bool{}
	for _, member := range members {
		if member.UserID != actor || member.TeamID == "" || member.TeamID != strings.TrimSpace(member.TeamID) {
			return subject, denied
		}
		if !requested[member.TeamID] {
			ids = append(ids, member.TeamID)
			requested[member.TeamID] = true
		}
	}
	if len(ids) != 0 {
		teamReader, ok := view.Team.(repo.IInboxTeamReader)
		if !ok {
			return subject, denied
		}
		teams, err := teamReader.GetInboxTeams(ctx, ids)
		if err != nil {
			return subject, err
		}
		seen := map[string]bool{}
		for _, team := range teams {
			if team == nil || !requested[team.TeamID] || seen[team.TeamID] {
				return subject, denied
			}
			seen[team.TeamID] = true
			if team.IsEnabled == 1 {
				subject.TeamIDs = append(subject.TeamIDs, team.TeamID)
			}
		}
	}
	return subject, ctx.Err()
}

func authorizeManualInboxTrigger(ctx context.Context, view *repo.Repositories, project *model.Project, pipeline *model.Pipeline, actor string) error {
	if project == nil || pipeline == nil || pipeline.ProjectID != project.ProjectID || project.IsEnabled != 1 || project.Status != model.ProjectStatusActive || pipeline.IsEnabled != 1 || pipeline.IsDeleted != 0 {
		return repo.ErrManualTriggerForbidden
	}
	subject, err := liveManualInboxSubject(ctx, view, actor)
	if err != nil {
		return err
	}
	checker, err := authz.NewLiveAuthorizer(ctx, view)
	if err != nil {
		return repo.ErrManualTriggerForbidden
	}
	for _, action := range []string{"project:read", "pipeline:read", "pipeline:trigger"} {
		scope := authz.ResourceRef{Type: "pipeline", ID: pipeline.PipelineID, ProjectID: project.ProjectID, OrgID: project.OrgID}
		if action == "project:read" {
			scope.Type, scope.ID = "project", project.ProjectID
		}
		allowed, err := checker.Check(ctx, subject, action, scope)
		if err != nil || !allowed {
			return repo.ErrManualTriggerForbidden
		}
	}
	return ctx.Err()
}
