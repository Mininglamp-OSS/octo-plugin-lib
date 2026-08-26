// Package pluginconformance provides a reusable behavioral suite for hosts
// that expose the shared Plugin Service contract.
package pluginconformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginservice"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
)

type API interface {
	Create(context.Context, pluginservice.Scope, pluginservice.Actor, pluginservice.CreateInput) (pluginservice.Snapshot, error)
	Get(context.Context, pluginservice.Scope, string) (pluginservice.Snapshot, error)
	List(context.Context, pluginservice.Scope, pluginservice.ListFilter) (pluginservice.PluginPage, error)
	Update(context.Context, pluginservice.Scope, pluginservice.Actor, string, pluginservice.UpdateInput) (pluginservice.Snapshot, error)
	SetStatus(context.Context, pluginservice.Scope, pluginservice.Actor, string, pluginservice.SetStatusInput) (pluginservice.Snapshot, error)
	ReplaceRelations(context.Context, pluginservice.Scope, pluginservice.Actor, string, pluginservice.ReplaceRelationsInput) (pluginservice.Snapshot, error)
	GetRevision(context.Context, pluginservice.Scope, string, uint32) (pluginservice.Revision, error)
	ListRevisions(context.Context, pluginservice.Scope, string, pluginservice.PageRequest) (pluginservice.RevisionPage, error)
	Restore(context.Context, pluginservice.Scope, pluginservice.Actor, string, uint32, pluginservice.RestoreInput) (pluginservice.Snapshot, error)
	CreateGraph(context.Context, pluginservice.Scope, pluginservice.Actor, pluginservice.GraphCreateInput) (pluginservice.GraphCreateResult, error)
}

func Run(t *testing.T, api API) {
	t.Helper()
	if api == nil {
		t.Fatal("plugin conformance: API is required")
	}
	ctx := context.Background()
	scope := pluginservice.Scope{ID: "scope-" + randomHex(t, 8)}
	actor := pluginservice.Actor{ID: "actor-conformance"}

	skillID := randomUUID(t)
	skillContent := content(contract.TypeSkill, "Conformance Skill", "current-description-original")
	skill, err := api.Create(ctx, scope, actor, pluginservice.CreateInput{PluginID: skillID, Content: skillContent})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if skill.Plugin.Status != contract.StatusActive || skill.Plugin.LockVersion != 1 ||
		skill.Plugin.CurrentRevisionNo != 1 || skill.Revision.RevisionNo != 1 ||
		skill.Relations == nil {
		t.Fatalf("Create result = %#v", skill)
	}
	if _, err := api.Get(ctx, pluginservice.Scope{ID: scope.ID + "-other"}, skillID); !errors.Is(err, pluginstore.ErrNotFound) {
		t.Fatalf("scope isolation error = %v", err)
	}
	if revision, err := api.GetRevision(ctx, scope, skillID, 1); err != nil || revision.RevisionNo != 1 {
		t.Fatalf("GetRevision = %#v, %v", revision, err)
	}

	noOp, err := api.Update(ctx, scope, actor, skillID, pluginservice.UpdateInput{
		ExpectedLockVersion: 1, Content: skillContent,
	})
	if err != nil || noOp.Plugin.LockVersion != 1 || noOp.Revision.RevisionNo != 1 ||
		!noOp.Plugin.UpdatedAt.Equal(skill.Plugin.UpdatedAt) {
		t.Fatalf("no-op Update = %#v, %v", noOp, err)
	}
	archived, err := api.SetStatus(ctx, scope, actor, skillID, pluginservice.SetStatusInput{
		Status: contract.StatusArchived, ExpectedLockVersion: 1,
	})
	if err != nil || archived.Plugin.LockVersion != 2 || archived.Revision.RevisionNo != 1 {
		t.Fatalf("SetStatus archive = %#v, %v", archived, err)
	}
	changedContent := content(contract.TypeSkill, "Conformance Skill v2", "historical-description-only")
	if _, err := api.Update(ctx, scope, actor, skillID, pluginservice.UpdateInput{
		ExpectedLockVersion: 2, Content: changedContent,
	}); !errors.Is(err, pluginstore.ErrConflict) {
		t.Fatalf("ARCHIVED content Update error = %v", err)
	}
	restored, err := api.Restore(ctx, scope, actor, skillID, 1, pluginservice.RestoreInput{ExpectedLockVersion: 2})
	if err != nil || restored.Plugin.LockVersion != 3 || restored.Revision.RevisionNo != 2 ||
		restored.Revision.PluginHash != skill.Revision.PluginHash ||
		restored.Plugin.Status != contract.StatusArchived {
		t.Fatalf("Restore = %#v, %v", restored, err)
	}
	active, err := api.SetStatus(ctx, scope, actor, skillID, pluginservice.SetStatusInput{
		Status: contract.StatusActive, ExpectedLockVersion: 3,
	})
	if err != nil || active.Plugin.LockVersion != 4 || active.Revision.RevisionNo != 2 {
		t.Fatalf("SetStatus active = %#v, %v", active, err)
	}
	changed, err := api.Update(ctx, scope, actor, skillID, pluginservice.UpdateInput{
		ExpectedLockVersion: 4, Content: changedContent,
	})
	if err != nil || changed.Plugin.LockVersion != 5 || changed.Revision.RevisionNo != 3 ||
		changed.Revision.PluginHash == skill.Revision.PluginHash ||
		changed.Plugin.PluginName != "Conformance Skill v2" {
		t.Fatalf("content Update = %#v, %v", changed, err)
	}
	if _, err := api.SetStatus(ctx, scope, actor, skillID, pluginservice.SetStatusInput{
		Status: contract.StatusArchived, ExpectedLockVersion: 4,
	}); !errors.Is(err, pluginstore.ErrConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	history, err := api.ListRevisions(ctx, scope, skillID, pluginservice.PageRequest{})
	if err != nil || history.Total != 3 || len(history.Items) != 3 ||
		history.Items[0].RevisionNo != 3 {
		t.Fatalf("ListRevisions = %#v, %v", history, err)
	}

	expertID := randomUUID(t)
	related, err := api.Create(ctx, scope, actor, pluginservice.CreateInput{
		PluginID: expertID,
		Content:  content(contract.TypeExpert, "Status-aware Expert", "current relation"),
		Relations: []pluginservice.RelationInput{{
			RelationType: contract.RelationExpertSkill, TargetPluginID: skillID,
		}},
	})
	if err != nil || len(related.Relations) != 1 {
		t.Fatalf("Create related Expert = %#v, %v", related, err)
	}
	same, err := api.ReplaceRelations(ctx, scope, actor, expertID, pluginservice.ReplaceRelationsInput{
		ExpectedLockVersion: 1,
		Relations: []pluginservice.RelationInput{{
			RelationType: contract.RelationExpertSkill, TargetPluginID: skillID,
		}},
	})
	if err != nil || same.Plugin.LockVersion != 1 {
		t.Fatalf("no-op ReplaceRelations = %#v, %v", same, err)
	}
	detached, err := api.ReplaceRelations(ctx, scope, actor, expertID, pluginservice.ReplaceRelationsInput{
		ExpectedLockVersion: 1, Relations: []pluginservice.RelationInput{},
	})
	if err != nil || detached.Plugin.LockVersion != 2 || len(detached.Relations) != 0 ||
		detached.Revision.RevisionNo != 1 {
		t.Fatalf("remove Relation = %#v, %v", detached, err)
	}
	archivedExpert, err := api.SetStatus(ctx, scope, actor, expertID, pluginservice.SetStatusInput{
		Status: contract.StatusArchived, ExpectedLockVersion: 2,
	})
	if err != nil {
		t.Fatalf("archive Expert: %v", err)
	}
	restoredExpert, err := api.Restore(ctx, scope, actor, expertID, 1, pluginservice.RestoreInput{
		ExpectedLockVersion: archivedExpert.Plugin.LockVersion,
	})
	if err != nil || restoredExpert.Revision.RevisionNo != 2 || len(restoredExpert.Relations) != 0 {
		t.Fatalf("Restore must preserve current Relations = %#v, %v", restoredExpert, err)
	}
	if _, err := api.ReplaceRelations(ctx, scope, actor, expertID, pluginservice.ReplaceRelationsInput{
		ExpectedLockVersion: restoredExpert.Plugin.LockVersion,
		Relations: []pluginservice.RelationInput{{
			RelationType: contract.RelationExpertSkill, TargetPluginID: skillID,
		}},
	}); !errors.Is(err, pluginstore.ErrConflict) {
		t.Fatalf("ARCHIVED source accepted new Relation: %v", err)
	}

	archivedSkill, err := api.SetStatus(ctx, scope, actor, skillID, pluginservice.SetStatusInput{
		Status: contract.StatusArchived, ExpectedLockVersion: changed.Plugin.LockVersion,
	})
	if err != nil {
		t.Fatalf("archive target: %v", err)
	}
	if _, err := api.Create(ctx, scope, actor, pluginservice.CreateInput{
		PluginID: randomUUID(t),
		Content:  content(contract.TypeExpert, "Blocked Expert", "archived target"),
		Relations: []pluginservice.RelationInput{{
			RelationType: contract.RelationExpertSkill, TargetPluginID: skillID,
		}},
	}); !errors.Is(err, pluginstore.ErrConflict) {
		t.Fatalf("Relation to ARCHIVED target error = %v", err)
	}
	status := contract.StatusArchived
	page, err := api.List(ctx, scope, pluginservice.ListFilter{PluginType: contract.TypeSkill, Status: &status})
	if err != nil || page.Total != 1 || len(page.Items) != 1 ||
		page.Items[0].PluginID != skillID || page.Items[0].LockVersion != archivedSkill.Plugin.LockVersion {
		t.Fatalf("List = %#v, %v", page, err)
	}

	literalID := randomUUID(t)
	if _, err := api.Create(ctx, scope, actor, pluginservice.CreateInput{
		PluginID: literalID, Content: content(contract.TypeSkill, "Literal %_ Skill", "literal search"),
	}); err != nil {
		t.Fatalf("Create literal-name Plugin: %v", err)
	}
	for _, test := range []struct {
		query string
		want  int64
	}{
		{"%_", 1},
		{"literal search", 1},
		{"historical-description-only", 1},
		{"current-description-original", 0},
		{"not-present-anywhere", 0},
	} {
		result, err := api.List(ctx, scope, pluginservice.ListFilter{Query: test.query})
		if err != nil || result.Total != test.want {
			t.Fatalf("List(%q) = %#v, %v", test.query, result, err)
		}
	}

	storageID := randomUUID(t)
	storageContent := pluginservice.ContentInput{
		PluginType:   contract.TypeSkill,
		ManifestJSON: json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Stored Skill","plugin_type":"skill","name":"Stored Skill","description":"storage"}`),
		PluginJSON:   json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"storage","mime_type":"text/markdown","content_size":7,"content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`),
	}
	stored, err := api.Create(ctx, scope, actor, pluginservice.CreateInput{PluginID: storageID, Content: storageContent})
	if err != nil || stored.Revision.PluginHash == "" {
		t.Fatalf("Create storage Plugin = %#v, %v", stored, err)
	}

	foreignScope := pluginservice.Scope{ID: scope.ID + "-foreign"}
	foreignTargetID := randomUUID(t)
	if _, err := api.Create(ctx, foreignScope, actor, pluginservice.CreateInput{
		PluginID: foreignTargetID, Content: content(contract.TypeSkill, "Foreign Skill", "foreign"),
	}); err != nil {
		t.Fatalf("Create foreign target: %v", err)
	}
	crossScopeSourceID := randomUUID(t)
	_, err = api.Create(ctx, scope, actor, pluginservice.CreateInput{
		PluginID: crossScopeSourceID,
		Content:  content(contract.TypeExpert, "Cross Scope Expert", "cross scope"),
		Relations: []pluginservice.RelationInput{{
			RelationType: contract.RelationExpertSkill, TargetPluginID: foreignTargetID,
		}},
	})
	if !errors.Is(err, pluginstore.ErrInvalidArgument) {
		t.Fatalf("cross-scope Relation error = %v", err)
	}
	if _, err := api.Get(ctx, scope, crossScopeSourceID); !errors.Is(err, pluginstore.ErrNotFound) {
		t.Fatalf("cross-scope Create was not rolled back: %v", err)
	}

	teamID, dependencyID, connectorID := randomUUID(t), randomUUID(t), randomUUID(t)
	const graphExpertID = "f0000000-0000-4000-8000-000000000001"
	graph, err := api.CreateGraph(ctx, scope, actor, pluginservice.GraphCreateInput{
		RootPluginID: teamID,
		Nodes: []pluginservice.GraphNodeInput{
			{PluginID: connectorID, Content: content(contract.TypeConnector, "Connector", "connector")},
			{PluginID: teamID, Content: content(contract.TypeExpertTeam, "Team", "team"), Relations: []pluginservice.RelationInput{{RelationType: contract.RelationExpertTeamExpert, TargetPluginID: graphExpertID}}},
			{PluginID: dependencyID, Content: content(contract.TypeSkill, "Dependency", "skill")},
			{PluginID: graphExpertID, Content: content(contract.TypeExpert, "Expert", "expert"), Relations: []pluginservice.RelationInput{{RelationType: contract.RelationExpertSkill, TargetPluginID: dependencyID}, {RelationType: contract.RelationExpertConnector, TargetPluginID: connectorID}}},
		},
	})
	if err != nil || graph.Root.Plugin.PluginID != teamID || len(graph.Items) != 4 ||
		len(graph.Root.Relations) != 1 {
		t.Fatalf("CreateGraph = %#v, %v", graph, err)
	}

	const rollbackID = "00000000-0000-0000-0000-000000000000"
	_, err = api.CreateGraph(ctx, scope, actor, pluginservice.GraphCreateInput{
		RootPluginID: rollbackID,
		Nodes: []pluginservice.GraphNodeInput{
			{PluginID: rollbackID, Content: content(contract.TypeExpertTeam, "Rollback Team", "rollback"), Relations: []pluginservice.RelationInput{{RelationType: contract.RelationExpertTeamExpert, TargetPluginID: graphExpertID}}},
			{PluginID: graphExpertID, Content: content(contract.TypeExpert, "Duplicate Expert", "duplicate")},
		},
	})
	if !errors.Is(err, pluginstore.ErrAlreadyExists) {
		t.Fatalf("failing CreateGraph error = %v", err)
	}
	if _, err := api.Get(ctx, scope, rollbackID); !errors.Is(err, pluginstore.ErrNotFound) {
		t.Fatalf("CreateGraph was not atomic: %v", err)
	}
}

func content(pluginType contract.Type, name, description string) pluginservice.ContentInput {
	manifest, _ := json.Marshal(map[string]any{
		"$schema": "cowork-plugin-manifest-2.0.json", "plugin_name": name,
		"plugin_type": pluginType, "name": name, "description": description,
	})
	packageJSON := json.RawMessage("null")
	if pluginType == contract.TypeSkill {
		packageJSON = json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill"}]}`)
	}
	return pluginservice.ContentInput{PluginType: pluginType, ManifestJSON: manifest, PluginJSON: packageJSON}
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func randomUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:])
}
