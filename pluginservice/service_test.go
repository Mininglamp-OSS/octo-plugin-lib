package pluginservice

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
)

const testPluginID = "10000000-0000-4000-8000-000000000001"

func TestCreateNormalizesContentAndOwnsInitialState(t *testing.T) {
	store := &stubStore{}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time {
		return time.Date(2026, 8, 25, 1, 2, 3, 999, time.FixedZone("offset", 8*60*60))
	}
	_, err = service.Create(context.Background(), Scope{ID: "scope-test"}, Actor{ID: "actor-test"}, CreateInput{
		PluginID: testPluginID,
		Content: ContentInput{
			PluginType:   contract.TypeSkill,
			ManifestJSON: []byte(`{"plugin_type":"skill","name":"Skill","description":"","plugin_name":"Skill","$schema":"cowork-plugin-manifest-2.0.json"}`),
			PluginJSON:   []byte(`{"attachments":[{"path":"SKILL.md","content_type":"storage","mime_type":"text/markdown","content_size":7,"content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"$schema":"cowork-plugin-package-2.0.json"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.created.Plugin.Status != contract.StatusActive ||
		store.created.Plugin.CurrentRevisionNo != 1 || store.created.Plugin.LockVersion != 1 ||
		store.created.Revision.RevisionNo != 1 {
		t.Fatalf("prepared record = %#v", store.created)
	}
	if !store.created.Plugin.CreatedAt.Equal(time.Date(2026, 8, 24, 17, 2, 3, 0, time.UTC)) {
		t.Fatalf("created_at = %s", store.created.Plugin.CreatedAt)
	}
	if string(store.created.Revision.ManifestJSON) != `{"$schema":"cowork-plugin-manifest-2.0.json","description":"","name":"Skill","plugin_name":"Skill","plugin_type":"skill"}` {
		t.Fatalf("Manifest was not canonicalized: %s", store.created.Revision.ManifestJSON)
	}
}

func TestCreateAcceptsSkillOnlyAttachmentsIndependentOfInputOrder(t *testing.T) {
	store := &stubStore{}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), Scope{ID: "scope-test"}, Actor{ID: "actor-test"}, CreateInput{
		PluginID: testPluginID,
		Content: ContentInput{
			PluginType:   contract.TypeConnector,
			ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill Connector","plugin_type":"connector","name":"Skill Connector","description":""}`),
			PluginJSON:   []byte(`{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"skill-only","source":"connector.skill"},"attachments":[{"path":"token-schema.json","content_type":"raw","mime_type":"application/json","raw_content":"{}"},{"path":"skills/example/SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Example"}]}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(store.created.Revision.PluginJSON); !strings.Contains(got, `"path":"skills/example/SKILL.md"`) || !strings.Contains(got, `"path":"token-schema.json"`) {
		t.Fatalf("normalized plugin_json lost required attachments: %s", got)
	}
}

func TestStatusAndScopeValidation(t *testing.T) {
	service, err := New(&stubStore{})
	if err != nil {
		t.Fatal(err)
	}
	invalidStatus := contract.Status("DISABLED")
	if _, err := service.SetStatus(context.Background(), Scope{ID: "scope-test"}, Actor{ID: "actor-test"},
		testPluginID, SetStatusInput{Status: invalidStatus, ExpectedLockVersion: 1}); !errors.Is(err, pluginstore.ErrInvalidArgument) {
		t.Fatalf("SetStatus invalid status error = %v", err)
	}
	if _, err := service.List(context.Background(), Scope{ID: strings.Repeat("x", 41)}, ListFilter{}); !errors.Is(err, pluginstore.ErrInvalidArgument) {
		t.Fatalf("List oversized scope error = %v", err)
	}
	if _, err := service.List(context.Background(), Scope{ID: "scope-test"}, ListFilter{Status: &invalidStatus}); !errors.Is(err, pluginstore.ErrInvalidArgument) {
		t.Fatalf("List invalid status error = %v", err)
	}
}

func TestServicePreservesContractValidationError(t *testing.T) {
	service, err := New(&stubStore{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), Scope{ID: "scope-test"}, Actor{ID: "actor-test"}, CreateInput{
		PluginID: testPluginID,
		Content: ContentInput{
			PluginType:   contract.TypeSkill,
			ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill"}`),
			PluginJSON:   []byte("null"),
		},
	})
	var violation *contract.ValidationError
	if !errors.Is(err, pluginstore.ErrInvalidArgument) || !errors.As(err, &violation) ||
		violation.Code != contract.CodeInvalidField || violation.Path != "manifest_json.description" {
		t.Fatalf("Create validation error = %v", err)
	}
}

func TestReplaceRelationsValidatesAndSorts(t *testing.T) {
	store := &stubStore{snapshot: pluginstore.Snapshot{Plugin: pluginstore.Plugin{
		ScopeID: "scope-test", PluginID: testPluginID, PluginType: contract.TypeExpert,
	}}}
	service, _ := New(store)
	_, err := service.ReplaceRelations(context.Background(), Scope{ID: "scope-test"}, Actor{ID: "actor-test"},
		testPluginID, ReplaceRelationsInput{ExpectedLockVersion: 1, Relations: []RelationInput{
			{RelationType: contract.RelationExpertSkill, TargetPluginID: "30000000-0000-4000-8000-000000000003"},
			{RelationType: contract.RelationExpertConnector, TargetPluginID: "20000000-0000-4000-8000-000000000002"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.relations.Relations; len(got) != 2 ||
		got[0].RelationType != contract.RelationExpertConnector ||
		got[1].RelationType != contract.RelationExpertSkill {
		t.Fatalf("Relations = %#v", got)
	}
	_, err = service.ReplaceRelations(context.Background(), Scope{ID: "scope-test"}, Actor{ID: "actor-test"},
		testPluginID, ReplaceRelationsInput{ExpectedLockVersion: 1, Relations: []RelationInput{
			{RelationType: contract.RelationExpertSkill, TargetPluginID: "30000000-0000-4000-8000-000000000003"},
			{RelationType: contract.RelationExpertSkill, TargetPluginID: "30000000-0000-4000-8000-000000000003"},
		}})
	if !errors.Is(err, pluginstore.ErrInvalidArgument) {
		t.Fatalf("duplicate Relation error = %v", err)
	}
}

func TestCreateGraphRequiresClosedRootedGraph(t *testing.T) {
	service, _ := New(&stubStore{})
	rootID := testPluginID
	orphanID := "10000000-0000-4000-8000-000000000002"
	_, err := service.CreateGraph(context.Background(), Scope{ID: "scope-test"}, Actor{ID: "actor-test"}, GraphCreateInput{
		RootPluginID: rootID,
		Nodes: []GraphNodeInput{
			{PluginID: rootID, Content: nullContent(contract.TypeExpertTeam, "Team")},
			{PluginID: orphanID, Content: nullContent(contract.TypeExpert, "Expert")},
		},
	})
	if !errors.Is(err, pluginstore.ErrInvalidArgument) {
		t.Fatalf("unreachable graph error = %v", err)
	}
}

func TestPublicServiceMethodsDoNotDrift(t *testing.T) {
	typeOf := reflect.TypeOf((*Service)(nil))
	got := make([]string, 0, typeOf.NumMethod())
	for index := 0; index < typeOf.NumMethod(); index++ {
		got = append(got, typeOf.Method(index).Name)
	}
	want := []string{"Create", "CreateGraph", "Get", "GetRevision", "List", "ListRevisions", "ReplaceRelations", "Restore", "SetStatus", "Update", "WithTx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Service methods = %v, want %v", got, want)
	}
}

func nullContent(pluginType contract.Type, name string) ContentInput {
	return ContentInput{
		PluginType: pluginType,
		ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"` + name +
			`","plugin_type":"` + string(pluginType) + `","name":"` + name + `","description":""}`),
		PluginJSON: []byte("null"),
	}
}

type stubStore struct {
	created   pluginstore.CreateRecord
	relations pluginstore.RelationsUpdateRecord
	snapshot  pluginstore.Snapshot
}

func (store *stubStore) WithTx(*sql.Tx) (pluginstore.Store, error) { return store, nil }
func (store *stubStore) Create(_ context.Context, record pluginstore.CreateRecord) (pluginstore.Snapshot, error) {
	store.created = record
	return pluginstore.Snapshot(record), nil
}
func (*stubStore) CreateGraph(context.Context, []pluginstore.CreateRecord) ([]pluginstore.Snapshot, error) {
	return nil, nil
}
func (store *stubStore) Get(context.Context, string, string) (pluginstore.Snapshot, error) {
	return store.snapshot, nil
}
func (*stubStore) List(context.Context, string, pluginstore.ListFilter) (pluginstore.PluginPage, error) {
	return pluginstore.PluginPage{}, nil
}
func (*stubStore) UpdateContent(context.Context, pluginstore.ContentUpdateRecord) (pluginstore.Snapshot, error) {
	return pluginstore.Snapshot{}, nil
}
func (*stubStore) SetStatus(context.Context, pluginstore.StatusUpdateRecord) (pluginstore.Snapshot, error) {
	return pluginstore.Snapshot{}, nil
}
func (store *stubStore) ReplaceRelations(_ context.Context, record pluginstore.RelationsUpdateRecord) (pluginstore.Snapshot, error) {
	store.relations = record
	return store.snapshot, nil
}
func (*stubStore) GetRevision(context.Context, string, string, uint32) (pluginstore.Revision, error) {
	return pluginstore.Revision{}, pluginstore.ErrNotFound
}
func (*stubStore) ListRevisions(context.Context, string, string, pluginstore.PageRequest) (pluginstore.RevisionPage, error) {
	return pluginstore.RevisionPage{}, nil
}
