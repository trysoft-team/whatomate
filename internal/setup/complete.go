package setup

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/config"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type databaseInput struct {
	Driver   string `json:"driver"`
	Path     string `json:"path"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Name     string `json:"name"`
	SSLMode  string `json:"ssl_mode"`
}

type completeRequest struct {
	targetRequest
	Organization string `json:"organization"`
	Admin        struct {
		FullName string `json:"full_name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	} `json:"admin"`
}

func (d databaseInput) config() config.DatabaseConfig {
	c := config.DatabaseConfig{
		Driver: strings.ToLower(strings.TrimSpace(d.Driver)), Path: strings.TrimSpace(d.Path),
		Host: strings.TrimSpace(d.Host), Port: d.Port, User: d.User, Password: d.Password,
		Name: strings.TrimSpace(d.Name), SSLMode: d.SSLMode,
	}
	switch c.Driver {
	case config.DriverPostgres:
		if c.Port == 0 {
			c.Port = 5432
		}
		if c.SSLMode == "" {
			c.SSLMode = "disable"
		}
	case config.DriverMySQL:
		if c.Port == 0 {
			c.Port = 3306
		}
	}
	return c
}

func validateDatabase(c config.DatabaseConfig) error {
	switch c.Driver {
	case config.DriverSQLite:
		if c.Path == "" || !filepath.IsAbs(c.Path) {
			return errors.New("SQLite needs an absolute file path")
		}
		return nil
	case config.DriverPostgres, config.DriverMySQL:
		switch {
		case c.Host == "":
			return errors.New("host is required")
		case c.Port < 1 || c.Port > 65535:
			return errors.New("database port must be between 1 and 65535")
		case c.User == "":
			return errors.New("user is required")
		case c.Name == "":
			return errors.New("database name is required")
		}
		return nil
	}
	return fmt.Errorf("unsupported database %q; choose sqlite, postgres or mysql", c.Driver)
}

// finish validates req, prepares the database and admin, and writes the
// config file last, so any failure leaves no config behind. It returns the
// HTTP status to use with the error.
func (s *server) finish(req completeRequest, https bool) (int, error) {
	tg, err := s.parseTarget(req.targetRequest)
	if err != nil {
		return fasthttp.StatusBadRequest, err
	}
	org := strings.TrimSpace(req.Organization)
	if org == "" || len(org) > 100 {
		return fasthttp.StatusBadRequest, errors.New("organization name is required (up to 100 characters)")
	}
	fullName := strings.TrimSpace(req.Admin.FullName)
	if fullName == "" {
		return fasthttp.StatusBadRequest, errors.New("full name is required")
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(req.Admin.Email))
	if err != nil {
		return fasthttp.StatusBadRequest, errors.New("enter a valid email address")
	}
	if len(req.Admin.Password) < 8 {
		return fasthttp.StatusBadRequest, errors.New("password must be at least 8 characters")
	}
	if len(req.Admin.Password) > 72 {
		return fasthttp.StatusBadRequest, errors.New("password must be at most 72 bytes")
	}
	if err := tg.checkFree(); err != nil {
		return fasthttp.StatusConflict, err
	}

	db, closeDB, code, err := openTarget(tg.db)
	if err != nil {
		return code, err
	}
	defer closeDB()
	dbTarget := databaseTarget(tg.db)
	adminID, err := s.reusableAdmin(db, dbTarget)
	if errors.Is(err, errHasUsers) {
		return fasthttp.StatusConflict, err
	}
	if err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	admin := config.DefaultAdminConfig{Email: addr.Address, Password: req.Admin.Password, FullName: fullName, OrganizationName: org}
	migrateErr := s.migrate(db, &admin)
	if adminID == uuid.Nil {
		// The database had no users, so a user with this email is the one this
		// attempt created. Recording it even when a later step failed lets a
		// retry reuse this database.
		var user models.User
		if err := db.Where("email = ?", admin.Email).First(&user).Error; err == nil {
			adminID = user.ID
		} else if migrateErr == nil {
			return fasthttp.StatusInternalServerError, fmt.Errorf("find admin user: %w", err)
		}
	}
	if adminID != uuid.Nil {
		s.seeded = &seededAdmin{target: dbTarget, userID: adminID}
	}
	if migrateErr != nil {
		return fasthttp.StatusInternalServerError, fmt.Errorf("set up the database: %w", migrateErr)
	}
	if err := applyAdmin(db, adminID, admin); err != nil {
		return fasthttp.StatusInternalServerError, err
	}
	if code, err := s.writeConfig(tg, https, ""); err != nil {
		return code, err
	}
	s.clearRestorePending(tg.cand)
	return fasthttp.StatusOK, nil
}

func configExists(path string) error {
	return fmt.Errorf("%s already exists; restart whatomate to use it", path)
}

var errHasUsers = errors.New("This database already has users. Choose an empty database.") //nolint:staticcheck // shown to the user as-is

// seededAdmin is an admin user setup created, and the database it is in.
type seededAdmin struct {
	target string
	userID uuid.UUID
}

// databaseTarget identifies the database c points at.
func databaseTarget(c config.DatabaseConfig) string {
	if c.Driver == config.DriverSQLite {
		return c.Driver + ":" + filepath.Clean(c.Path)
	}
	return c.Driver + "://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) + "/" + c.Name
}

// reusableAdmin returns uuid.Nil when db has no users, or the admin an
// earlier attempt created in this same database. Any other users make it
// fail with errHasUsers. Call with completeMu held.
func (s *server) reusableAdmin(db *gorm.DB, target string) (uuid.UUID, error) {
	if !db.Migrator().HasTable(&models.User{}) {
		return uuid.Nil, nil
	}
	var n int64
	if err := db.Unscoped().Model(&models.User{}).Count(&n).Error; err != nil {
		return uuid.Nil, fmt.Errorf("check existing users: %w", err)
	}
	if n == 0 {
		return uuid.Nil, nil
	}
	if s.seeded == nil || s.seeded.target != target {
		return uuid.Nil, errHasUsers
	}
	var mine int64
	if err := db.Model(&models.User{}).Where("id = ?", s.seeded.userID).Count(&mine).Error; err != nil {
		return uuid.Nil, fmt.Errorf("check existing users: %w", err)
	}
	if mine == 0 || n != 1 {
		return uuid.Nil, errHasUsers
	}
	return s.seeded.userID, nil
}

// applyAdmin sets the wizard's email, password, name and organization name
// on admin id, so a retry uses the retry's values.
func applyAdmin(db *gorm.DB, id uuid.UUID, a config.DefaultAdminConfig) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(a.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	var user models.User
	if err := db.First(&user, "id = ?", id).Error; err != nil {
		return fmt.Errorf("find admin user: %w", err)
	}
	if err := db.Model(&user).Updates(map[string]any{"email": a.Email, "password_hash": string(hash), "full_name": a.FullName}).Error; err != nil {
		return fmt.Errorf("update admin user: %w", err)
	}
	if err := db.Model(&models.Organization{}).Where("id = ?", user.OrganizationID).Update("name", a.OrganizationName).Error; err != nil {
		return fmt.Errorf("update organization: %w", err)
	}
	return nil
}
