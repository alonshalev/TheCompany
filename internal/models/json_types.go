package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONMap is a map[string]any that can be stored in a JSONB column.
type JSONMap map[string]any

func (j JSONMap) Value() (driver.Value, error) {
	if j == nil {
		return "{}", nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (j *JSONMap) Scan(src any) error {
	var source []byte
	switch v := src.(type) {
	case string:
		source = []byte(v)
	case []byte:
		source = v
	case nil:
		*j = JSONMap{}
		return nil
	default:
		return fmt.Errorf("JSONMap: cannot scan %T", src)
	}
	out := make(map[string]any)
	if err := json.Unmarshal(source, &out); err != nil {
		return err
	}
	*j = out
	return nil
}

// JSONSlice is a []any that can be stored in a JSONB column.
type JSONSlice []any

func (j JSONSlice) Value() (driver.Value, error) {
	if j == nil {
		return "[]", nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (j *JSONSlice) Scan(src any) error {
	var source []byte
	switch v := src.(type) {
	case string:
		source = []byte(v)
	case []byte:
		source = v
	case nil:
		*j = JSONSlice{}
		return nil
	default:
		return fmt.Errorf("JSONSlice: cannot scan %T", src)
	}
	var out []any
	if err := json.Unmarshal(source, &out); err != nil {
		return err
	}
	*j = out
	return nil
}
