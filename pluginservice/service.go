package pluginservice

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
)

type Service struct {
	store pluginstore.Store
	now   func() time.Time
}

func New(store pluginstore.Store) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: Store is required", pluginstore.ErrInvalidArgument)
	}
	return &Service{store: store, now: time.Now}, nil
}

// WithTx lets a host commit Plugin state with its own audit, idempotency or
// projection rows. The caller owns commit and rollback; each library write is
// isolated by a savepoint as described by pluginstore.Store.WithTx.
func (service *Service) WithTx(tx *sql.Tx) (*Service, error) {
	if service == nil || service.store == nil || tx == nil {
		return nil, fmt.Errorf("%w: transaction is required", pluginstore.ErrInvalidArgument)
	}
	store, err := service.store.WithTx(tx)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, now: service.now}, nil
}

func (service *Service) Create(ctx context.Context, scope Scope, actor Actor, input CreateInput) (Snapshot, error) {
	if err := validateScopeActor(scope, actor); err != nil {
		return Snapshot{}, err
	}
	if err := contract.ValidatePluginID(input.PluginID); err != nil {
		return Snapshot{}, invalid(err)
	}
	now := service.nowUTC()
	revision, name, err := prepareContent(scope.ID, input.PluginID, actor.ID, now, input.Content)
	if err != nil {
		return Snapshot{}, err
	}
	relations, err := prepareRelations(scope.ID, input.PluginID, input.Content.PluginType, input.Relations)
	if err != nil {
		return Snapshot{}, err
	}
	revision.RevisionNo = 1
	return service.store.Create(ctx, pluginstore.CreateRecord{
		Plugin: pluginstore.Plugin{
			ScopeID: scope.ID, PluginID: input.PluginID, PluginName: name,
			PluginType: input.Content.PluginType, Status: contract.StatusActive,
			CurrentRevisionNo: 1, LockVersion: 1, CreatedAt: now, UpdatedAt: now,
		},
		Revision:  revision,
		Relations: relations,
	})
}

func (service *Service) Get(ctx context.Context, scope Scope, pluginID string) (Snapshot, error) {
	if err := validateScopePlugin(scope, pluginID); err != nil {
		return Snapshot{}, err
	}
	return service.store.Get(ctx, scope.ID, pluginID)
}

func (service *Service) List(ctx context.Context, scope Scope, filter ListFilter) (PluginPage, error) {
	if err := validateIdentifier("scope_id", scope.ID, 40); err != nil {
		return PluginPage{}, err
	}
	if filter.Page == 0 {
		filter.Page = 1
	}
	if filter.PageSize == 0 {
		filter.PageSize = 20
	}
	if filter.Page < 1 || filter.PageSize < 1 || filter.PageSize > 100 {
		return PluginPage{}, fmt.Errorf("%w: invalid pagination", pluginstore.ErrInvalidArgument)
	}
	if filter.PluginType != "" && !filter.PluginType.Valid() {
		return PluginPage{}, fmt.Errorf("%w: invalid plugin_type", pluginstore.ErrInvalidArgument)
	}
	if filter.Status != nil && !contract.IsValidStatus(*filter.Status) {
		return PluginPage{}, fmt.Errorf("%w: invalid status", pluginstore.ErrInvalidArgument)
	}
	filter.Query = strings.TrimSpace(filter.Query)
	if !utf8.ValidString(filter.Query) || utf8.RuneCountInString(filter.Query) > 160 {
		return PluginPage{}, fmt.Errorf("%w: query is invalid", pluginstore.ErrInvalidArgument)
	}
	return service.store.List(ctx, scope.ID, filter)
}

func (service *Service) Update(ctx context.Context, scope Scope, actor Actor, pluginID string, input UpdateInput) (Snapshot, error) {
	if err := validateScopeActor(scope, actor); err != nil {
		return Snapshot{}, err
	}
	if err := validatePluginAndLock(pluginID, input.ExpectedLockVersion); err != nil {
		return Snapshot{}, err
	}
	now := service.nowUTC()
	revision, name, err := prepareContent(scope.ID, pluginID, actor.ID, now, input.Content)
	if err != nil {
		return Snapshot{}, err
	}
	return service.store.UpdateContent(ctx, pluginstore.ContentUpdateRecord{
		ScopeID: scope.ID, PluginID: pluginID, PluginName: name,
		PluginType: input.Content.PluginType, ExpectedLockVersion: input.ExpectedLockVersion,
		UpdatedAt: now, Revision: revision,
	})
}

func (service *Service) SetStatus(ctx context.Context, scope Scope, pluginID string, input SetStatusInput) (Snapshot, error) {
	if err := validateScopePlugin(scope, pluginID); err != nil {
		return Snapshot{}, err
	}
	if input.ExpectedLockVersion == 0 {
		return Snapshot{}, fmt.Errorf("%w: expected_lock_version is required", pluginstore.ErrInvalidArgument)
	}
	if err := contract.ValidateStatus(input.Status); err != nil {
		return Snapshot{}, invalid(err)
	}
	return service.store.SetStatus(ctx, pluginstore.StatusUpdateRecord{
		ScopeID: scope.ID, PluginID: pluginID, Status: input.Status,
		ExpectedLockVersion: input.ExpectedLockVersion, UpdatedAt: service.nowUTC(),
	})
}

func (service *Service) ReplaceRelations(ctx context.Context, scope Scope, pluginID string, input ReplaceRelationsInput) (Snapshot, error) {
	if err := validateScopePlugin(scope, pluginID); err != nil {
		return Snapshot{}, err
	}
	if input.ExpectedLockVersion == 0 {
		return Snapshot{}, fmt.Errorf("%w: expected_lock_version is required", pluginstore.ErrInvalidArgument)
	}
	current, err := service.store.Get(ctx, scope.ID, pluginID)
	if err != nil {
		return Snapshot{}, err
	}
	relations, err := prepareRelations(scope.ID, pluginID, current.Plugin.PluginType, input.Relations)
	if err != nil {
		return Snapshot{}, err
	}
	return service.store.ReplaceRelations(ctx, pluginstore.RelationsUpdateRecord{
		ScopeID: scope.ID, PluginID: pluginID, Relations: relations,
		ExpectedLockVersion: input.ExpectedLockVersion, UpdatedAt: service.nowUTC(),
	})
}

func (service *Service) GetRevision(ctx context.Context, scope Scope, pluginID string, revisionNo uint32) (Revision, error) {
	if err := validateScopePlugin(scope, pluginID); err != nil {
		return Revision{}, err
	}
	if revisionNo == 0 {
		return Revision{}, fmt.Errorf("%w: revision_no is required", pluginstore.ErrInvalidArgument)
	}
	return service.store.GetRevision(ctx, scope.ID, pluginID, revisionNo)
}

func (service *Service) ListRevisions(ctx context.Context, scope Scope, pluginID string, page PageRequest) (RevisionPage, error) {
	if err := validateScopePlugin(scope, pluginID); err != nil {
		return RevisionPage{}, err
	}
	if page.Page == 0 {
		page.Page = 1
	}
	if page.PageSize == 0 {
		page.PageSize = 20
	}
	if page.Page < 1 || page.PageSize < 1 || page.PageSize > 100 {
		return RevisionPage{}, fmt.Errorf("%w: invalid pagination", pluginstore.ErrInvalidArgument)
	}
	return service.store.ListRevisions(ctx, scope.ID, pluginID, page)
}

func (service *Service) Restore(ctx context.Context, scope Scope, actor Actor, pluginID string, revisionNo uint32, input RestoreInput) (Snapshot, error) {
	if err := validateScopeActor(scope, actor); err != nil {
		return Snapshot{}, err
	}
	if err := validatePluginAndLock(pluginID, input.ExpectedLockVersion); err != nil {
		return Snapshot{}, err
	}
	if revisionNo == 0 {
		return Snapshot{}, fmt.Errorf("%w: revision_no is required", pluginstore.ErrInvalidArgument)
	}
	source, err := service.store.GetRevision(ctx, scope.ID, pluginID, revisionNo)
	if err != nil {
		return Snapshot{}, err
	}
	now := service.nowUTC()
	prepared, name, err := prepareContent(scope.ID, pluginID, actor.ID, now, ContentInput{
		PluginType: source.PluginType, ManifestJSON: source.ManifestJSON, PluginJSON: source.PluginJSON,
	})
	if err != nil {
		return Snapshot{}, err
	}
	return service.store.UpdateContent(ctx, pluginstore.ContentUpdateRecord{
		ScopeID: scope.ID, PluginID: pluginID, PluginName: name,
		PluginType: source.PluginType, ExpectedLockVersion: input.ExpectedLockVersion,
		UpdatedAt: now, Revision: prepared, ForceRevision: true,
	})
}

func (service *Service) CreateGraph(ctx context.Context, scope Scope, actor Actor, input GraphCreateInput) (GraphCreateResult, error) {
	if err := validateScopeActor(scope, actor); err != nil {
		return GraphCreateResult{}, err
	}
	if len(input.Nodes) == 0 {
		return GraphCreateResult{}, fmt.Errorf("%w: graph is empty", pluginstore.ErrInvalidArgument)
	}
	if err := contract.ValidatePluginID(input.RootPluginID); err != nil {
		return GraphCreateResult{}, invalid(err)
	}
	nodes := make(map[string]GraphNodeInput, len(input.Nodes))
	for _, node := range input.Nodes {
		if err := contract.ValidatePluginID(node.PluginID); err != nil {
			return GraphCreateResult{}, invalid(err)
		}
		if _, duplicate := nodes[node.PluginID]; duplicate {
			return GraphCreateResult{}, fmt.Errorf("%w: duplicate graph plugin_id", pluginstore.ErrInvalidArgument)
		}
		nodes[node.PluginID] = node
	}
	if _, exists := nodes[input.RootPluginID]; !exists || !closedRootedGraph(input.RootPluginID, nodes) {
		return GraphCreateResult{}, fmt.Errorf("%w: graph must be closed and every node reachable from root", pluginstore.ErrInvalidArgument)
	}
	now := service.nowUTC()
	records := make([]pluginstore.CreateRecord, 0, len(input.Nodes))
	for _, node := range input.Nodes {
		revision, name, err := prepareContent(scope.ID, node.PluginID, actor.ID, now, node.Content)
		if err != nil {
			return GraphCreateResult{}, err
		}
		relations, err := prepareRelations(scope.ID, node.PluginID, node.Content.PluginType, node.Relations)
		if err != nil {
			return GraphCreateResult{}, err
		}
		for _, relation := range relations {
			target := nodes[relation.TargetPluginID]
			if err := contract.ValidateRelationEndpoints(contract.Relation{
				SourcePluginID: node.PluginID, TargetPluginID: relation.TargetPluginID,
				RelationType: relation.RelationType,
			}, node.Content.PluginType, target.Content.PluginType); err != nil {
				return GraphCreateResult{}, invalid(err)
			}
		}
		revision.RevisionNo = 1
		records = append(records, pluginstore.CreateRecord{
			Plugin: pluginstore.Plugin{
				ScopeID: scope.ID, PluginID: node.PluginID, PluginName: name,
				PluginType: node.Content.PluginType, Status: contract.StatusActive,
				CurrentRevisionNo: 1, LockVersion: 1, CreatedAt: now, UpdatedAt: now,
			},
			Revision: revision, Relations: relations,
		})
	}
	items, err := service.store.CreateGraph(ctx, records)
	if err != nil {
		return GraphCreateResult{}, err
	}
	result := GraphCreateResult{Items: items}
	for _, item := range items {
		if item.Plugin.PluginID == input.RootPluginID {
			result.Root = item
			break
		}
	}
	return result, nil
}

func prepareContent(scopeID, pluginID, actorID string, createdAt time.Time, input ContentInput) (pluginstore.Revision, string, error) {
	content, err := contract.NormalizeRevisionContent(contract.RevisionContent{
		PluginType: input.PluginType, ManifestJSON: input.ManifestJSON, PluginJSON: input.PluginJSON,
	})
	if err != nil {
		return pluginstore.Revision{}, "", invalid(err)
	}
	manifest, err := contract.DecodeManifest(content.ManifestJSON)
	if err != nil {
		return pluginstore.Revision{}, "", invalid(err)
	}
	return pluginstore.Revision{
		ScopeID: scopeID, PluginID: pluginID, PluginType: input.PluginType,
		ManifestJSON: content.ManifestJSON, PluginJSON: content.PluginJSON,
		PluginHash: content.PluginHash, CreatedBy: actorID, CreatedAt: createdAt,
	}, manifest.PluginName, nil
}

func prepareRelations(scopeID, pluginID string, sourceType contract.Type, input []RelationInput) ([]pluginstore.Relation, error) {
	relations := make([]pluginstore.Relation, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		relation := contract.Relation{
			SourcePluginID: pluginID, TargetPluginID: item.TargetPluginID, RelationType: item.RelationType,
		}
		if err := contract.ValidateRelation(relation); err != nil {
			return nil, invalid(err)
		}
		if sourceType == contract.TypeSkill || sourceType == contract.TypeConnector ||
			sourceType == contract.TypeExpertTeam && item.RelationType != contract.RelationExpertTeamExpert ||
			sourceType == contract.TypeExpert && item.RelationType != contract.RelationExpertSkill &&
				item.RelationType != contract.RelationExpertConnector {
			return nil, fmt.Errorf("%w: relation_type is invalid for source Plugin", pluginstore.ErrInvalidArgument)
		}
		key := string(item.RelationType) + "\x00" + item.TargetPluginID
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Relation", pluginstore.ErrInvalidArgument)
		}
		seen[key] = struct{}{}
		relations = append(relations, pluginstore.Relation{
			ScopeID: scopeID, SourcePluginID: pluginID,
			RelationType: item.RelationType, TargetPluginID: item.TargetPluginID,
		})
	}
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].RelationType == relations[j].RelationType {
			return relations[i].TargetPluginID < relations[j].TargetPluginID
		}
		return relations[i].RelationType < relations[j].RelationType
	})
	return relations, nil
}

func closedRootedGraph(root string, nodes map[string]GraphNodeInput) bool {
	edges := make(map[string][]string, len(nodes))
	for sourceID, node := range nodes {
		for _, relation := range node.Relations {
			if _, exists := nodes[relation.TargetPluginID]; !exists {
				return false
			}
			edges[sourceID] = append(edges[sourceID], relation.TargetPluginID)
		}
	}
	visited := make(map[string]struct{}, len(nodes))
	queue := []string{root}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, exists := visited[current]; exists {
			continue
		}
		visited[current] = struct{}{}
		queue = append(queue, edges[current]...)
	}
	return len(visited) == len(nodes)
}

func (service *Service) nowUTC() time.Time {
	return service.now().UTC().Truncate(time.Microsecond)
}

func validateScopeActor(scope Scope, actor Actor) error {
	if err := validateIdentifier("scope_id", scope.ID, 40); err != nil {
		return err
	}
	return validateIdentifier("actor_id", actor.ID, 191)
}

func validateScopePlugin(scope Scope, pluginID string) error {
	if err := validateIdentifier("scope_id", scope.ID, 40); err != nil {
		return err
	}
	if err := contract.ValidatePluginID(pluginID); err != nil {
		return invalid(err)
	}
	return nil
}

func validatePluginAndLock(pluginID string, lockVersion uint32) error {
	if err := contract.ValidatePluginID(pluginID); err != nil {
		return invalid(err)
	}
	if lockVersion == 0 {
		return fmt.Errorf("%w: expected_lock_version is required", pluginstore.ErrInvalidArgument)
	}
	return nil
}

func validateIdentifier(field, value string, maximum int) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is invalid", pluginstore.ErrInvalidArgument, field)
	}
	for _, character := range value {
		if character > unicode.MaxASCII || !(unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("._:-", character)) {
			return fmt.Errorf("%w: %s is invalid", pluginstore.ErrInvalidArgument, field)
		}
	}
	return nil
}

func invalid(err error) error {
	return fmt.Errorf("%w: %w", pluginstore.ErrInvalidArgument, err)
}
