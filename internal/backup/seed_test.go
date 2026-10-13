package backup

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

var seedTime = time.Date(2026, 10, 13, 9, 30, 15, 123456789, time.UTC)

// seedEveryTable puts three rows into every backed-up table with a value in
// every column, parents first so foreign keys resolve. Row 1 has every bool
// false; row 2 is soft-deleted; messages form a reply chain.
func seedEveryTable(t *testing.T, db *gorm.DB, precision time.Duration) {
	t.Helper()
	tbls, err := backupTables(db)
	require.NoError(t, err)
	fks, err := foreignKeys(db)
	require.NoError(t, err)
	byName := map[string]table{}
	var names []string
	for _, tb := range tbls {
		byName[tb.Name] = tb
		names = append(names, tb.Name)
	}
	refs := map[string]map[string]foreignKey{}
	for _, fk := range fks {
		if refs[fk.Table] == nil {
			refs[fk.Table] = map[string]foreignKey{}
		}
		refs[fk.Table][fk.Column] = fk
	}
	inserted := map[string][]map[string]any{}
	for _, name := range planLoad(names, fks).Order {
		tb := byName[name]
		for i := 0; i < 3; i++ {
			row := map[string]any{}
			for _, f := range tb.Columns {
				if fk, ok := refs[name][f.DBName]; ok {
					parents := inserted[fk.RefTable]
					switch {
					case fk.RefTable == name && i == 0:
						row[f.DBName] = nil
					case fk.RefTable == name:
						row[f.DBName] = inserted[name][i-1][fk.RefColumn]
					case len(parents) == 0:
						row[f.DBName] = nil
					default:
						row[f.DBName] = parents[i%len(parents)][fk.RefColumn]
					}
					continue
				}
				row[f.DBName] = sampleValue(f, i, precision)
			}
			require.NoError(t, insertRows(db, name, []map[string]any{row}), "seed %s row %d", name, i)
			inserted[name] = append(inserted[name], row)
		}
	}
}

func sampleValue(f *schema.Field, i int, precision time.Duration) any {
	if f.Schema.Table == "user_passkeys" && f.DBName == "credential_id" {
		// base64url only; MySQL stores it as ASCII.
		return fmt.Sprintf("cred-%d", i)
	}
	v := sampleOf(f.FieldType, f.DBName, f.Size, i, precision)
	if v == nil && f.NotNull {
		v = sampleOf(f.FieldType, f.DBName, f.Size, 1, precision)
	}
	return v
}

func sampleOf(typ reflect.Type, name string, size, i int, precision time.Duration) any {
	when := seedTime.Add(time.Duration(i) * time.Hour).Truncate(precision)
	switch typ {
	case reflect.TypeOf(uuid.UUID{}):
		return uuid.New()
	case reflect.TypeOf(time.Time{}):
		return when
	case reflect.TypeOf(gorm.DeletedAt{}):
		if i == 2 {
			return gorm.DeletedAt{Time: when, Valid: true}
		}
		return gorm.DeletedAt{}
	case reflect.TypeOf(models.JSONB{}):
		if i == 0 {
			return nil
		}
		return models.JSONB{"text": "héllo ✓", "n": float64(i), "nested": map[string]any{"ok": true}}
	case reflect.TypeOf(models.JSONBArray{}):
		return models.JSONBArray{"a", float64(i)}
	case reflect.TypeOf(models.StringArray{}):
		return models.StringArray{"x", fmt.Sprint(i)}
	}
	switch typ.Kind() {
	case reflect.Pointer:
		if i == 0 {
			return nil
		}
		v := reflect.New(typ.Elem())
		if inner := sampleOf(typ.Elem(), name, size, i, precision); inner != nil {
			v.Elem().Set(reflect.ValueOf(inner).Convert(typ.Elem()))
		}
		return v.Interface()
	case reflect.String:
		s := []rune(fmt.Sprintf("%d-%s-ü", i, name))
		if size > 0 && len(s) > size {
			s = s[:size]
		}
		return reflect.ValueOf(string(s)).Convert(typ).Interface()
	case reflect.Bool:
		return reflect.ValueOf(i%2 == 0).Convert(typ).Interface()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(int64(i*10 + 7)).Convert(typ).Interface()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.ValueOf(uint64(i*10 + 7)).Convert(typ).Interface()
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(1.25 + float64(i)).Convert(typ).Interface()
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return []byte{0, byte(i), 255}
		}
	}
	return reflect.Zero(typ).Interface()
}

// dumpTables returns every backed-up row of every table, encoded and sorted,
// for comparing two databases.
func dumpTables(t *testing.T, db *gorm.DB) map[string][]string {
	t.Helper()
	tbls, err := backupTables(db)
	require.NoError(t, err)
	out := map[string][]string{}
	for _, tb := range tbls {
		rows, err := db.Unscoped().Model(reflect.New(tb.Schema.ModelType).Interface()).Rows()
		require.NoError(t, err)
		for rows.Next() {
			item := reflect.New(tb.Schema.ModelType)
			require.NoError(t, db.ScanRows(rows, item.Interface()))
			line, err := encodeRow(context.Background(), tb.Columns, item.Elem())
			require.NoError(t, err)
			out[tb.Name] = append(out[tb.Name], string(line))
		}
		require.NoError(t, rows.Close())
		sort.Strings(out[tb.Name])
	}
	return out
}
