package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trysoft/whatomate/internal/models"
)

func TestRestore_RoundTripEveryTable(t *testing.T) {
	src := newSQLiteDB(t)
	seedEveryTable(t, src, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)

	dst := newSQLiteDB(t)
	stale := models.Organization{Name: "stale", Slug: "stale-" + uuid.NewString()[:8]}
	require.NoError(t, dst.Create(&stale).Error)
	require.NoError(t, dst.Exec("INSERT INTO kv_entries (kv_key, kv_value) VALUES ('x', 'y')").Error)

	var phases []string
	require.NoError(t, Restore(context.Background(), dst, filepath.Join(dir, info.Name), RestoreOptions{}, func(p Progress) { phases = append(phases, p.Phase) }))
	assert.Contains(t, phases, PhaseLoad)

	want, got := dumpTables(t, src), dumpTables(t, dst)
	for name := range want {
		assert.Equal(t, want[name], got[name], name)
	}
	var n int64
	require.NoError(t, dst.Unscoped().Model(&models.Organization{}).Where("id = ?", stale.ID).Count(&n).Error)
	assert.Zero(t, n, "existing data is replaced")
	require.NoError(t, dst.Table("kv_entries").Count(&n).Error)
	assert.Zero(t, n, "store tables are emptied")

	var inactive int64
	require.NoError(t, dst.Unscoped().Model(&models.User{}).Where("is_active = ?", false).Count(&inactive).Error)
	assert.Positive(t, inactive, "false survives a column whose default is true")
}

func TestRestore_OverPopulatedDatabase(t *testing.T) {
	src := newSQLiteDB(t)
	seedEveryTable(t, src, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)

	dst := newSQLiteDB(t)
	seedEveryTable(t, dst, time.Nanosecond)
	before := dumpTables(t, dst)
	want := dumpTables(t, src)
	require.NotEqual(t, want["organizations"], before["organizations"], "the two databases start out different")

	require.NoError(t, Restore(context.Background(), dst, filepath.Join(dir, info.Name), RestoreOptions{}, nil))
	got := dumpTables(t, dst)
	require.Equal(t, len(want), len(got))
	for name := range want {
		assert.Equal(t, want[name], got[name], name)
	}
}

// editTable rewrites one table's rows (and its manifest entry) in a backup.
func editTable(t *testing.T, path, name string, edit func(rows []map[string]json.RawMessage) []map[string]json.RawMessage, dropColumn string) string {
	t.Helper()
	var newData []byte
	out := rewriteBackup(t, path, func(entry string, data []byte) ([]byte, bool) {
		if entry != "db/"+name+".jsonl" {
			return data, true
		}
		var rows []map[string]json.RawMessage
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var r map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(line, &r))
			if dropColumn != "" {
				delete(r, dropColumn)
			}
			rows = append(rows, r)
		}
		var buf bytes.Buffer
		for _, r := range edit(rows) {
			b, _ := json.Marshal(r)
			buf.Write(append(b, '\n'))
		}
		newData = buf.Bytes()
		return newData, true
	})
	sum := sha256.Sum256(newData)
	return rewriteBackup(t, out, editManifest(t, func(m *Manifest) {
		for i := range m.Tables {
			if m.Tables[i].Name != name {
				continue
			}
			m.Tables[i].SHA256 = hex.EncodeToString(sum[:])
			m.Tables[i].Rows = int64(bytes.Count(newData, []byte("\n")))
			if dropColumn != "" {
				var cols []string
				for _, c := range m.Tables[i].Columns {
					if c != dropColumn {
						cols = append(cols, c)
					}
				}
				m.Tables[i].Columns = cols
			}
		}
	}))
}

func TestRestore_FailureChangesNothing(t *testing.T) {
	src := newSQLiteDB(t)
	seedEveryTable(t, src, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	broken := editTable(t, filepath.Join(dir, info.Name), "users", func(rows []map[string]json.RawMessage) []map[string]json.RawMessage {
		rows[0]["organization_id"] = json.RawMessage(`"` + uuid.NewString() + `"`)
		return rows
	}, "")

	dst := newSQLiteDB(t)
	keep := models.Organization{Name: "keep", Slug: "keep-" + uuid.NewString()[:8]}
	require.NoError(t, dst.Create(&keep).Error)
	err = Restore(context.Background(), dst, broken, RestoreOptions{}, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "users"), err.Error())
	var n int64
	require.NoError(t, dst.Model(&models.Organization{}).Where("id = ?", keep.ID).Count(&n).Error)
	assert.Equal(t, int64(1), n, "a failed restore rolls back")
}

func TestRestore_OlderBackupWithoutAColumn(t *testing.T) {
	src := newSQLiteDB(t)
	seedEveryTable(t, src, time.Nanosecond)
	dir := t.TempDir()
	info, err := Create(context.Background(), src, dir, Options{Kind: KindManual}, nil)
	require.NoError(t, err)
	older := editTable(t, filepath.Join(dir, info.Name), "users", func(rows []map[string]json.RawMessage) []map[string]json.RawMessage { return rows }, "is_available")

	dst := newSQLiteDB(t)
	require.NoError(t, Restore(context.Background(), dst, older, RestoreOptions{}, nil))
	var unavailable int64
	require.NoError(t, dst.Unscoped().Model(&models.User{}).Where("is_available = ?", false).Count(&unavailable).Error)
	assert.Zero(t, unavailable, "a column missing from the backup gets its default (true)")
}
