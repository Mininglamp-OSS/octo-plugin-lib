package pluginstore_test

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
)

func TestStableErrorCodes(t *testing.T) {
	tests := []struct {
		err  error
		code pluginstore.ErrorCode
	}{
		{pluginstore.ErrInvalidArgument, pluginstore.CodeInvalidArgument},
		{pluginstore.ErrNotFound, pluginstore.CodeNotFound},
		{pluginstore.ErrAlreadyExists, pluginstore.CodeAlreadyExists},
		{pluginstore.ErrConflict, pluginstore.CodeConflict},
		{pluginstore.ErrIntegrity, pluginstore.CodeIntegrity},
		{pluginstore.ErrStorage, pluginstore.CodeInternal},
		{fmt.Errorf("%w: %w", pluginstore.ErrConflict, pluginstore.ErrTransactionAborted), pluginstore.CodeInternal},
	}
	for _, test := range tests {
		if got := pluginstore.Code(fmt.Errorf("wrapped: %w", test.err)); got != test.code {
			t.Errorf("Code(%v) = %s, want %s", test.err, got, test.code)
		}
	}
}

func TestPersistenceTypesDoNotDrift(t *testing.T) {
	tests := []struct {
		name   string
		typeOf reflect.Type
		want   []string
	}{
		{"Plugin", reflect.TypeOf(pluginstore.Plugin{}), []string{"CreatedAt", "CurrentRevisionNo", "LockVersion", "PluginID", "PluginName", "PluginType", "ScopeID", "Status", "UpdatedAt"}},
		{"Revision", reflect.TypeOf(pluginstore.Revision{}), []string{"CreatedAt", "CreatedBy", "ManifestJSON", "PluginHash", "PluginID", "PluginJSON", "PluginType", "RevisionNo", "ScopeID"}},
		{"Relation", reflect.TypeOf(pluginstore.Relation{}), []string{"RelationType", "ScopeID", "SourcePluginID", "TargetPluginID"}},
	}
	for _, test := range tests {
		got := make([]string, test.typeOf.NumField())
		for index := range got {
			got[index] = test.typeOf.Field(index).Name
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s fields = %v, want %v", test.name, got, test.want)
		}
	}
}
