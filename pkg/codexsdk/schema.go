package codexsdk

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type outputSchemaFile struct {
	Path    string
	cleanup func()
}

func createOutputSchemaFile(schema any) (outputSchemaFile, error) {
	if schema == nil {
		return outputSchemaFile{cleanup: func() {}}, nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return outputSchemaFile{}, err
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return outputSchemaFile{}, err
	}
	if object == nil {
		return outputSchemaFile{}, errors.New("outputSchema must be a plain JSON object")
	}
	dir, err := os.MkdirTemp("", "codex-output-schema-")
	if err != nil {
		return outputSchemaFile{}, err
	}
	cleanup := func() {
		_ = os.RemoveAll(dir)
	}
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		cleanup()
		return outputSchemaFile{}, err
	}
	return outputSchemaFile{Path: path, cleanup: cleanup}, nil
}

func (f outputSchemaFile) Cleanup() {
	if f.cleanup != nil {
		f.cleanup()
	}
}
