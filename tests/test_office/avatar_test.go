package test_office

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/golang-jwt/jwt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
	officeController "github.com/hngprojects/telex_be/pkg/controller/office"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/utility"
)

// avatarFakeDriver answers the office-enabled check and counts writes, so a
// rejected payload can be shown never to reach the service.
type avatarFakeDriver struct {
	execs *int
}

type avatarFakeConn struct{ driver avatarFakeDriver }

func (c *avatarFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &avatarFakeStmt{query: query, driver: c.driver}, nil
}
func (c *avatarFakeConn) Close() error              { return nil }
func (c *avatarFakeConn) Begin() (driver.Tx, error) { return nil, io.EOF }

type avatarFakeStmt struct {
	query  string
	driver avatarFakeDriver
}

func (s *avatarFakeStmt) Close() error  { return nil }
func (s *avatarFakeStmt) NumInput() int { return -1 }

func (s *avatarFakeStmt) Exec([]driver.Value) (driver.Result, error) {
	*s.driver.execs++
	return driver.RowsAffected(0), nil
}

func (s *avatarFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	if strings.Contains(s.query, "FROM organisations") {
		return &accessFakeRows{columns: []string{"enabled"}, values: [][]driver.Value{{true}}}, nil
	}
	return &accessFakeRows{}, nil
}

func (d avatarFakeDriver) Open(string) (driver.Conn, error) { return &avatarFakeConn{driver: d}, nil }

func avatarTestDB(t *testing.T, execs *int) *gorm.DB {
	t.Helper()

	name := "office-avatar-" + t.Name()
	sql.Register(name, avatarFakeDriver{execs: execs})

	conn, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("failed to open the fake database: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn:             conn,
		DriverName:       name,
		WithoutReturning: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("failed to build the fake database: %v", err)
	}

	return db
}

// The request carries validation tags for every avatar field. Without a
// Validator call in the handler, any string was persisted and later shipped in
// presence payloads. Invalid input must fail with 422 and never reach the
// service, so the stored avatar is untouched.
func TestSaveAvatarRejectsInvalidPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	execs := 0
	controller := officeController.Controller{
		Db:        &storage.Database{Postgresql: avatarTestDB(t, &execs)},
		Validator: validator.New(),
		Logger:    utility.NewLogger(),
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/users/me/avatar",
		strings.NewReader(`{"tint":"not-a-colour","body":"z","outfit":"x","accessory":"y"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("userClaims", jwt.MapClaims{"user_id": "usr-1", "org_id": "org-1"})

	controller.SaveAvatar(c)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422 (body: %s)", recorder.Code, recorder.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode the response: %v", err)
	}
	if body["message"] != "validation failed" {
		t.Fatalf("message: got %q, want %q", body["message"], "validation failed")
	}

	details, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error details missing, got %v", body["error"])
	}
	foundTint := false
	for field := range details {
		if strings.HasSuffix(field, "Tint") {
			foundTint = true
		}
	}
	if !foundTint {
		t.Fatalf("error details should name the tint field, got %v", details)
	}

	if execs != 0 {
		t.Fatalf("an invalid avatar reached the database (%d writes)", execs)
	}
}

// Guard for the fixture above: the same tags accept the values the client
// actually sends.
func TestOfficeAvatarRequestAcceptsValidPayload(t *testing.T) {
	err := validator.New().Struct(models.OfficeAvatarRequest{
		Tint:      "#aabbcc",
		Body:      "a",
		Outfit:    "casual",
		Accessory: "none",
	})
	if err != nil {
		t.Fatalf("a valid avatar must pass validation: %v", err)
	}
}
