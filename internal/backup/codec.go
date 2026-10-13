package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// encodeRow writes one row as a JSON object keyed by column name. Values are
// encoded from the model's Go types, so the result does not depend on the
// database it was read from; times are written in UTC.
func encodeRow(ctx context.Context, cols []*schema.Field, rv reflect.Value) ([]byte, error) {
	obj := make(map[string]json.RawMessage, len(cols))
	for _, f := range cols {
		v, _ := f.ValueOf(ctx, rv)
		b, err := json.Marshal(utc(v))
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", f.DBName, err)
		}
		obj[f.DBName] = b
	}
	return json.Marshal(obj)
}

func utc(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.UTC()
	case *time.Time:
		if t != nil {
			u := t.UTC()
			return &u
		}
	case gorm.DeletedAt:
		if t.Valid {
			t.Time = t.Time.UTC()
		}
		return t
	}
	return v
}

// decodeRow turns a backed-up row into values of each column's Go type, ready
// to insert. A column the model does not have is an error.
func decodeRow(line []byte, cols map[string]*schema.Field) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(raw))
	for name, val := range raw {
		f, ok := cols[name]
		if !ok {
			return nil, fmt.Errorf("unknown column %q", name)
		}
		ptr := reflect.New(f.FieldType)
		if err := json.Unmarshal(val, ptr.Interface()); err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		v := ptr.Elem()
		if v.Kind() == reflect.Pointer && v.IsNil() {
			out[name] = nil
			continue
		}
		out[name] = v.Interface()
	}
	return out, nil
}
