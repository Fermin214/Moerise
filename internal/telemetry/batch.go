package telemetry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"

	"github.com/google/jsonschema-go/jsonschema"
)

const MaxBytes = 48 * 1024

//go:embed schema-v1.json
var Schema []byte

var contract = func() *jsonschema.Resolved {
	var schema jsonschema.Schema
	if err := json.Unmarshal(Schema, &schema); err != nil {
		panic(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		panic(err)
	}
	return resolved
}()

type Batch struct {
	Schema        int    `json:"schema"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version"`
	CoreVersion   string `json:"core_version"`
	SystemMajor   string `json:"system_major"`
	Rows          []Row  `json:"rows"`
}

type Row struct {
	Operation string `json:"operation"`
	Result    string `json:"result"`
	Duration  string `json:"duration"`
	Source    string `json:"source"`
	Count     int    `json:"count"`
}

func ReadBatch(input io.Reader) (Batch, error) {
	var batch Batch
	data, err := io.ReadAll(io.LimitReader(input, MaxBytes+1))
	if err != nil || len(data) > MaxBytes {
		return batch, errors.New("invalid batch size")
	}
	value, err := objectValue(json.NewDecoder(bytes.NewReader(data)), 0)
	if err != nil || contract.Validate(value) != nil {
		return batch, errors.New("invalid batch")
	}
	err = json.Unmarshal(data, &batch)
	return batch, err
}

func objectValue(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil || depth > 3 {
		return nil, errors.New("invalid batch structure")
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if _, exists := object[name]; err != nil || !ok || exists {
				return nil, errors.New("invalid batch field")
			}
			value, err := objectValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		_, err = decoder.Token()
		return object, err
	case json.Delim('['):
		var values []any
		for decoder.More() {
			value, err := objectValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err = decoder.Token()
		return values, err
	default:
		return token, nil
	}
}
