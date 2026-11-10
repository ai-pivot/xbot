package config

import (
	"reflect"
	"strings"
	"testing"

	"xbot/protocol"
)

// structFieldByJSONTag finds the field of t whose `json` tag name equals tag.
// Only exported fields participate.
func structFieldByJSONTag(t reflect.Type, tag string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			name = f.Name
		}
		if name == tag {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// unwrapPointer strips any number of pointer/interface indirections so a
// *bool field (e.g. plugins.enabled) can be checked against ftBool.
func unwrapPointer(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// kindMatchesFieldType reports whether a struct field's (unwrapped) kind is
// the one the coercion schema must be written against.
func kindMatchesFieldType(ft fieldType, k reflect.Kind) bool {
	switch ft {
	case ftInt:
		return k == reflect.Int
	case ftInt64:
		return k == reflect.Int64
	case ftFloat:
		return k == reflect.Float64
	case ftBool:
		return k == reflect.Bool
	}
	return false
}

// TestConfigTypeSchemaReferencesOnlyExistingFields is the drift guard for the
// string→type coercion schema.
//
// configTypeSchema exists solely so normalizeConfigTypes can fix config files
// written by install scripts (`jq --arg` always emits strings). Every entry
// therefore names a field of a Config section. When a struct field is removed
// but its schema entry survives, the entry is silently dead — and worse, it
// makes the schema look like an authoritative field list while disagreeing with
// the struct. That already happened: `agent.max_concurrency` and
// `agent.purge_old_messages` were deleted from AgentConfig (max_concurrency has
// a single storage location in user_settings; purge_old_messages was dropped)
// but stayed in the schema.
//
// This test fails the moment a schema entry names a field the struct does not
// have, so the two can no longer drift apart unnoticed.
func TestConfigTypeSchemaReferencesOnlyExistingFields(t *testing.T) {
	cfgType := reflect.TypeOf(Config{})

	for section, fields := range configTypeSchema {
		sectionField, ok := structFieldByJSONTag(cfgType, section)
		if !ok {
			t.Errorf("configTypeSchema section %q has no matching field on Config", section)
			continue
		}
		sectionType := unwrapPointer(sectionField.Type)
		if sectionType.Kind() != reflect.Struct {
			t.Errorf("configTypeSchema section %q: Config field is %s, not a struct", section, sectionField.Type)
			continue
		}
		for field, ft := range fields {
			member, ok := structFieldByJSONTag(sectionType, field)
			if !ok {
				t.Errorf("configTypeSchema[%q][%q]: no such field on %s "+
					"(schema drift: the struct field was removed or renamed)", section, field, sectionType)
				continue
			}
			if got := unwrapPointer(member.Type).Kind(); !kindMatchesFieldType(ft, got) {
				t.Errorf("configTypeSchema[%q][%q]: schema declares %v but the field is %s",
					section, field, ft, member.Type)
			}
		}
	}
}

// TestSubscriptionTypeSchemaReferencesOnlyExistingFields is the same guard for
// subscriptionTypeSchema. That schema is applied to BOTH a subscription object
// and each entry of its per_model_configs map (see normalizeConfigTypes), so a
// key is valid when it exists on either protocol type.
func TestSubscriptionTypeSchemaReferencesOnlyExistingFields(t *testing.T) {
	subType := reflect.TypeOf(protocol.Subscription{})
	pmcType := reflect.TypeOf(protocol.PerModelConfig{})

	for field, ft := range subscriptionTypeSchema {
		member, okSub := structFieldByJSONTag(subType, field)
		if !okSub {
			member, okSub = structFieldByJSONTag(pmcType, field)
		}
		if !okSub {
			t.Errorf("subscriptionTypeSchema[%q]: no such field on %s or %s "+
				"(schema drift: the struct field was removed or renamed)",
				field, subType, pmcType)
			continue
		}
		if got := unwrapPointer(member.Type).Kind(); !kindMatchesFieldType(ft, got) {
			t.Errorf("subscriptionTypeSchema[%q]: schema declares %v but the field is %s",
				field, ft, member.Type)
		}
	}
}
