package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/blueship581/gbcarenotify/internal/dto"
	"github.com/blueship581/gbcarenotify/internal/model"
	"github.com/blueship581/gbcarenotify/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func setupConfirmService(t *testing.T) (*RecipientService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewRecipientService(db, repository.NewRecipientRepository(db), repository.NewReplyConfirmRepository(db), logger)
	return service, db
}

func createRecipient(t *testing.T, service *RecipientService, phone string) *model.CareRecipient {
	t.Helper()
	item, err := service.Create(context.Background(), dto.CreateRecipientRequest{Name: "王阿姨", Phone: phone, CareFrequency: "daily", CareStartAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func countConfirms(t *testing.T, db *gorm.DB, messageID string) int64 {
	t.Helper()
	var total int64
	if err := db.Model(&model.ReplyConfirm{}).Where("message_id = ?", messageID).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	return total
}

func TestConfirmIdempotentReplay(t *testing.T) {
	service, db := setupConfirmService(t)
	item := createRecipient(t, service, "13800138000")
	req := dto.ConfirmRecipientRequest{MessageID: "msg-001", Channel: "sms", Note: "回复平安"}
	first, err := service.Confirm(context.Background(), item.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.LastConfirmedAt == nil {
		t.Fatal("first confirmation did not store timestamp")
	}
	time.Sleep(10 * time.Millisecond)
	replayed, err := service.Confirm(context.Background(), item.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.LastConfirmedAt.Equal(*first.LastConfirmedAt) {
		t.Fatalf("replay returned %v, want first result %v", replayed.LastConfirmedAt, first.LastConfirmedAt)
	}
	if total := countConfirms(t, db, "msg-001"); total != 1 {
		t.Fatalf("confirm records=%d, want 1", total)
	}
	stored, err := service.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LastConfirmedAt.Equal(*first.LastConfirmedAt) {
		t.Fatalf("last confirmed advanced to %v, want unchanged %v", stored.LastConfirmedAt, first.LastConfirmedAt)
	}
}

func TestConfirmIdempotentConflict(t *testing.T) {
	service, _ := setupConfirmService(t)
	item := createRecipient(t, service, "13800138001")
	other := createRecipient(t, service, "13800138002")
	req := dto.ConfirmRecipientRequest{MessageID: "msg-002", Channel: "sms", Note: "回复平安"}
	if _, err := service.Confirm(context.Background(), item.ID, req); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		id   uint
		req  dto.ConfirmRecipientRequest
	}{
		{"different note", item.ID, dto.ConfirmRecipientRequest{MessageID: "msg-002", Channel: "sms", Note: "改口"}},
		{"different channel", item.ID, dto.ConfirmRecipientRequest{MessageID: "msg-002", Channel: "webhook", Note: "回复平安"}},
		{"different recipient", other.ID, dto.ConfirmRecipientRequest{MessageID: "msg-002", Channel: "sms", Note: "回复平安"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := service.Confirm(context.Background(), tt.id, tt.req); !errors.Is(err, repository.ErrConflict) {
				t.Fatalf("err=%v, want ErrConflict", err)
			}
		})
	}
	stored, err := service.Get(context.Background(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastConfirmedAt != nil {
		t.Fatalf("conflicted confirm advanced last confirmed at to %v", stored.LastConfirmedAt)
	}
}

func TestConfirmWriteFailureDoesNotAdvanceTime(t *testing.T) {
	service, db := setupConfirmService(t)
	item := createRecipient(t, service, "13800138003")
	if err := db.Migrator().DropTable(&model.ReplyConfirm{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(context.Background(), item.ID, dto.ConfirmRecipientRequest{Channel: "manual"}); err == nil {
		t.Fatal("expected confirm to fail when reply_confirms is missing")
	}
	stored, err := service.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastConfirmedAt != nil {
		t.Fatalf("failed confirm advanced last confirmed at to %v", stored.LastConfirmedAt)
	}
}

func TestConfirmConcurrentFirstSubmission(t *testing.T) {
	service, db := setupConfirmService(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	item := createRecipient(t, service, "13800138004")
	const workers = 8
	var wg sync.WaitGroup
	results := make([]*model.CareRecipient, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = service.Confirm(context.Background(), item.ID, dto.ConfirmRecipientRequest{MessageID: "msg-003", Channel: "sms", Note: "回复平安"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d err=%v, want success", i, err)
		}
	}
	for i, result := range results {
		if !result.LastConfirmedAt.Equal(*results[0].LastConfirmedAt) {
			t.Fatalf("worker %d got %v, want first result %v", i, result.LastConfirmedAt, results[0].LastConfirmedAt)
		}
	}
	if total := countConfirms(t, db, "msg-003"); total != 1 {
		t.Fatalf("confirm records=%d, want 1", total)
	}
}

func TestConfirmConcurrentDifferentPayloads(t *testing.T) {
	service, db := setupConfirmService(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	item := createRecipient(t, service, "13800138005")
	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded, conflicts := 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := service.Confirm(context.Background(), item.ID, dto.ConfirmRecipientRequest{MessageID: "msg-004", Channel: "sms", Note: fmt.Sprintf("note-%d", i)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, repository.ErrConflict):
				conflicts++
			default:
				t.Errorf("unexpected err=%v", err)
			}
		}(i)
	}
	wg.Wait()
	if succeeded != 1 || conflicts != workers-1 {
		t.Fatalf("succeeded=%d conflicts=%d, want 1 and %d", succeeded, conflicts, workers-1)
	}
	if total := countConfirms(t, db, "msg-004"); total != 1 {
		t.Fatalf("confirm records=%d, want 1", total)
	}
}

func TestConfirmWithoutMessageIDStillWorks(t *testing.T) {
	service, db := setupConfirmService(t)
	item := createRecipient(t, service, "13800138006")
	for i := 0; i < 2; i++ {
		if _, err := service.Confirm(context.Background(), item.ID, dto.ConfirmRecipientRequest{Channel: "manual", Note: "电话确认"}); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	if err := db.Model(&model.ReplyConfirm{}).Where("care_recipient_id = ?", item.ID).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("confirm records=%d, want 2", total)
	}
	stored, err := service.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastConfirmedAt == nil {
		t.Fatal("last confirmed at was not stored")
	}
}
