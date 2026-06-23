package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaDocPath is the published JSON Schema relative to this package directory.
const schemaDocPath = "../../docs/schema/wsstat-output-v1.schema.json"

// TestSchemaDocDrift guards the published JSON Schema against drift from the code: the
// schema's declared version must match JSONSchemaVersion, and the record types it lists must
// match the set the code actually emits. It does not run a full validator (no extra dep); it
// pins the contract surface so a renamed/added record or a version bump can't land silently.
func TestSchemaDocDrift(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(schemaDocPath))
	require.NoError(t, err, "published schema doc must exist")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))

	assert.Equal(t, JSONSchemaVersion, doc["x-schema-version"],
		"schema doc x-schema-version out of sync with JSONSchemaVersion")

	defs, ok := doc["$defs"].(map[string]any)
	require.True(t, ok, "schema doc must have $defs")

	schemaVersionDef, ok := defs["schemaVersion"].(map[string]any)
	require.True(t, ok, "schema doc must define schemaVersion")
	assert.Equal(t, JSONSchemaVersion, schemaVersionDef["const"],
		"schema doc schemaVersion const out of sync with JSONSchemaVersion")

	// The record `type` discriminators the schema declares must match what the code emits.
	got := recordTypeConsts(defs)
	slices.Sort(got)
	want := []string{
		"error", "response", "subscription_message", "subscription_summary", "timing",
	}
	assert.Equal(t, want, got, "schema doc record types out of sync with emitted records")
}

// recordTypeConsts extracts the `type` const of every record branch defined in $defs.
func recordTypeConsts(defs map[string]any) []string {
	var out []string
	for _, def := range defs {
		obj, ok := def.(map[string]any)
		if !ok {
			continue
		}
		props, ok := obj["properties"].(map[string]any)
		if !ok {
			continue
		}
		typeProp, ok := props["type"].(map[string]any)
		if !ok {
			continue
		}
		if c, ok := typeProp["const"].(string); ok {
			out = append(out, c)
		}
	}
	return out
}
