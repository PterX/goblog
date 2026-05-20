//go:build integration

package provider

import (
	"context"
	"log"
	"log/slog"
	"sync"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"kandaoni.com/anqicms/model"
)

// initTestDB initializes a MySQL test database connection using environment variables.
// If the database is not available, it skips the test.
func initTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "root:root@tcp(127.0.0.1:3306)/anqicms_test?charset=utf8mb4&parseTime=True&loc=Local"

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
	})
	if err != nil {
		t.Skipf("MySQL not available (dsn=%s): %v", dsn, err)
	}

	// Auto-migrate required tables
	if err := db.AutoMigrate(&model.Category{}, &model.Tag{}, &model.Archive{}, &model.ArchiveData{}); err != nil {
		t.Fatalf("failed to migrate tables: %v", err)
	}

	t.Cleanup(func() {
		// Clean up test data
		db.Exec("DELETE FROM archives")
		db.Exec("DELETE FROM categories")
		db.Exec("DELETE FROM tags")
		db.Exec("DELETE FROM archive_datas")
		sqlDB, err := db.DB()
		if err == nil {
			sqlDB.Close()
		}
	})

	return db
}

// testServiceWithDB creates an AiChatService with a real database for integration testing.
func testServiceWithDB(t *testing.T, db *gorm.DB) *AiChatService {
	t.Helper()
	web := &Website{
		DB: db,
	}
	svc := &AiChatService{
		mu:       sync.RWMutex{},
		sessions: make(map[string]*ChatSession),
		Logger:   slog.Default(),
		mcpSrv:   nil,
		db:       db,
		site:     web,
	}
	svc.Tools, svc.Handlers = svc.getEinoTools()
	return svc
}

func TestIntegration_ArchiveCRUD(t *testing.T) {
	db := initTestDB(t)
	svc := testServiceWithDB(t, db)
	ctx := context.Background()

	// First create a category (required for archive)
	catID, err := createTestCategory(db, "Test Category")
	if err != nil {
		log.Fatal(err)
	}
	t.Logf("Created test category: id=%d", catID)

	// 1. Create archive
	result, err := svc.Handlers["archive_create"](ctx, `{"title":"Integration Test Article","content":"This is test content for integration testing.","category_id":1}`)
	if err != nil {
		t.Fatalf("archive_create failed: %v", err)
	}
	t.Logf("archive_create result: %s", result)

	// 2. List archives
	result, err = svc.Handlers["archive_list"](ctx, `{}`)
	if err != nil {
		t.Fatalf("archive_list failed: %v", err)
	}
	t.Logf("archive_list result: %s", result)

	// 3. Get archive detail (archive_id=1)
	result, err = svc.Handlers["archive_get"](ctx, `{"archive_id":1}`)
	if err != nil {
		t.Fatalf("archive_get failed: %v", err)
	}
	t.Logf("archive_get result: %s", result)

	// 4. Publish archive
	result, err = svc.Handlers["archive_publish"](ctx, `{"archive_id":1,"status":1}`)
	if err != nil {
		t.Fatalf("archive_publish failed: %v", err)
	}
	t.Logf("archive_publish result: %s", result)

	// 5. Delete archive
	result, err = svc.Handlers["archive_delete"](ctx, `{"archive_id":1}`)
	if err != nil {
		t.Fatalf("archive_delete failed: %v", err)
	}
	t.Logf("archive_delete result: %s", result)
}

func TestIntegration_CategoryCRUD(t *testing.T) {
	db := initTestDB(t)
	svc := testServiceWithDB(t, db)
	ctx := context.Background()

	// 1. Create category
	result, err := svc.Handlers["category_create"](ctx, `{"title":"Technology","description":"Tech news and articles"}`)
	if err != nil {
		t.Fatalf("category_create failed: %v", err)
	}
	t.Logf("category_create result: %s", result)

	// 2. List categories
	result, err = svc.Handlers["category_list"](ctx, `{}`)
	if err != nil {
		t.Fatalf("category_list failed: %v", err)
	}
	t.Logf("category_list result: %s", result)

	// 3. Get category detail
	result, err = svc.Handlers["category_get"](ctx, `{"category_id":1}`)
	if err != nil {
		t.Fatalf("category_get failed: %v", err)
	}
	t.Logf("category_get result: %s", result)

	// 4. Delete category
	result, err = svc.Handlers["category_delete"](ctx, `{"category_id":1}`)
	if err != nil {
		t.Fatalf("category_delete failed: %v", err)
	}
	t.Logf("category_delete result: %s", result)
}

func TestIntegration_TagCRUD(t *testing.T) {
	db := initTestDB(t)
	svc := testServiceWithDB(t, db)
	ctx := context.Background()

	// 1. Create tag
	result, err := svc.Handlers["tag_create"](ctx, `{"title":"golang","description":"Go programming language"}`)
	if err != nil {
		t.Fatalf("tag_create failed: %v", err)
	}
	t.Logf("tag_create result: %s", result)

	// 2. List tags
	result, err = svc.Handlers["tag_list"](ctx, `{}`)
	if err != nil {
		t.Fatalf("tag_list failed: %v", err)
	}
	t.Logf("tag_list result: %s", result)

	// 3. Get tag detail
	result, err = svc.Handlers["tag_get"](ctx, `{"tag_id":1}`)
	if err != nil {
		t.Fatalf("tag_get failed: %v", err)
	}
	t.Logf("tag_get result: %s", result)

	// 4. Delete tag
	result, err = svc.Handlers["tag_delete"](ctx, `{"tag_id":1}`)
	if err != nil {
		t.Fatalf("tag_delete failed: %v", err)
	}
	t.Logf("tag_delete result: %s", result)
}

// createTestCategory creates a category directly via GORM (helper for integration tests).
func createTestCategory(db *gorm.DB, title string) (uint, error) {
	cat := &model.Category{
		Title:  title,
		Status: 1,
		Type:   1,
	}
	if err := db.Create(cat).Error; err != nil {
		return 0, err
	}
	return cat.Id, nil
}
