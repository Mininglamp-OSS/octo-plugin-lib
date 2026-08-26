package mysqlstore

import (
	"errors"
	"testing"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
)

func TestNewRelationRequiresActiveSourceAndTarget(t *testing.T) {
	relation := pluginstore.Relation{
		SourcePluginID: "10000000-0000-4000-8000-000000000001",
		RelationType:   contract.RelationExpertSkill,
		TargetPluginID: "20000000-0000-4000-8000-000000000002",
	}
	activeTarget := map[string]lockedPlugin{relation.TargetPluginID: {pluginType: contract.TypeSkill, status: contract.StatusActive}}
	archivedTarget := map[string]lockedPlugin{relation.TargetPluginID: {pluginType: contract.TypeSkill, status: contract.StatusArchived}}

	if err := validateRelations(contract.TypeExpert, contract.StatusActive, nil, []pluginstore.Relation{relation}, activeTarget); err != nil {
		t.Fatalf("ACTIVE relation rejected: %v", err)
	}
	for name, err := range map[string]error{
		"archived source": validateRelations(contract.TypeExpert, contract.StatusArchived, nil, []pluginstore.Relation{relation}, activeTarget),
		"archived target": validateRelations(contract.TypeExpert, contract.StatusActive, nil, []pluginstore.Relation{relation}, archivedTarget),
	} {
		if !errors.Is(err, pluginstore.ErrConflict) {
			t.Errorf("%s error = %v", name, err)
		}
	}
	if err := validateRelations(contract.TypeExpert, contract.StatusActive, nil, []pluginstore.Relation{relation}, nil); !errors.Is(err, pluginstore.ErrInvalidArgument) {
		t.Fatalf("missing target error = %v", err)
	}
	if err := validateRelations(contract.TypeExpert, contract.StatusActive, []pluginstore.Relation{relation}, []pluginstore.Relation{relation}, archivedTarget); err != nil {
		t.Fatalf("retained historical relation rejected: %v", err)
	}
}

func TestRelationTypeValidationPreservesContractDetails(t *testing.T) {
	relation := pluginstore.Relation{
		SourcePluginID: "10000000-0000-4000-8000-000000000001",
		RelationType:   contract.RelationExpertSkill,
		TargetPluginID: "20000000-0000-4000-8000-000000000002",
	}
	err := validateRelations(contract.TypeExpert, contract.StatusActive, nil, []pluginstore.Relation{relation}, map[string]lockedPlugin{
		relation.TargetPluginID: {pluginType: contract.TypeExpert, status: contract.StatusActive},
	})
	var violation *contract.ValidationError
	if !errors.Is(err, pluginstore.ErrInvalidArgument) || !errors.As(err, &violation) {
		t.Fatalf("error = %v, want invalid argument with contract details", err)
	}
	if violation.Code != contract.CodeInvalidRelation || violation.Path != "relation_type" {
		t.Fatalf("validation error = %#v", violation)
	}
}
