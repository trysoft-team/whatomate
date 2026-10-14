package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SQLKV is a KV stored in the kv_entries table of the main database.
type SQLKV struct {
	db   *gorm.DB
	stop chan struct{}
	once sync.Once
}

// NewSQLKV returns a database-backed KV and starts a janitor that deletes
// expired rows every 10 minutes; call Close to stop it.
func NewSQLKV(db *gorm.DB) *SQLKV {
	s := &SQLKV{db: db, stop: make(chan struct{})}
	go s.janitor(10 * time.Minute)
	return s
}

// Close stops the janitor.
func (s *SQLKV) Close() { s.once.Do(func() { close(s.stop) }) }

func (s *SQLKV) janitor(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			_ = s.PurgeExpired(ctx)
			cancel()
		}
	}
}

// PurgeExpired deletes expired rows.
func (s *SQLKV) PurgeExpired(ctx context.Context) error {
	return s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now()).
		Delete(&models.KVEntry{}).Error
}

func (s *SQLKV) live(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Where("expires_at IS NULL OR expires_at > ?", time.Now())
}

func (s *SQLKV) Get(ctx context.Context, key string) ([]byte, error) {
	var row models.KVEntry
	err := s.live(ctx).Where("kv_key = ?", key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row.Value, nil
}

func (s *SQLKV) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	row := models.KVEntry{Key: key, Value: val}
	if ttl > 0 {
		exp := time.Now().Add(ttl)
		row.ExpiresAt = &exp
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kv_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"kv_value", "expires_at"}),
	}).Create(&row).Error
}

func (s *SQLKV) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("kv_key IN ?", keys).Delete(&models.KVEntry{}).Error
}

func (s *SQLKV) GetDel(ctx context.Context, key string) ([]byte, error) {
	val, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	res := s.db.WithContext(ctx).Where("kv_key = ?", key).Delete(&models.KVEntry{})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrNotFound // another caller took it first
	}
	return val, nil
}

func (s *SQLKV) DelPrefix(ctx context.Context, prefix string) error {
	q := s.db.WithContext(ctx)
	if s.db.Name() == config.DriverSQLite {
		// SQLite's LIKE ignores ASCII case; GLOB does not.
		q = q.Where("kv_key GLOB ?", escapeGlob(prefix)+"*")
	} else {
		q = q.Where("kv_key LIKE ? ESCAPE '!'", escapeLike(prefix)+"%")
	}
	return q.Delete(&models.KVEntry{}).Error
}

func (s *SQLKV) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// escapeLike escapes LIKE wildcards using '!' as the escape character.
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// escapeGlob escapes SQLite GLOB wildcards by wrapping each in a bracket set.
func escapeGlob(s string) string {
	return strings.NewReplacer("*", "[*]", "?", "[?]", "[", "[[]").Replace(s)
}
