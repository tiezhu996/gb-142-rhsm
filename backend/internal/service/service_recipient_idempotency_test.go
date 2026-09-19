package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/blueship581/gbcarenotify/internal/dto"
	"github.com/blueship581/gbcarenotify/internal/model"
	"github.com/blueship581/gbcarenotify/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newIdempotencyService(t *testing.T) (*RecipientService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	// A shared in-memory SQLite only exists per connection; pin the pool to a
	// single connection so concurrent goroutines see the same database.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewRecipientService(db, repository.NewRecipientRepository(db), repository.NewReplyConfirmRepository(db), logger)
	return svc, db
}

func createTestRecipient(t *testing.T, svc *RecipientService, phone string) *model.CareRecipient {
	t.Helper()
	item, err := svc.Create(context.Background(), dto.CreateRecipientRequest{
		Name: phone, Phone: phone, CareFrequency: "daily",
		CareStartAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestExternalConfirmReplayReturnsFirstResult(t *testing.T) {
	svc, db := newIdempotencyService(t)
	recipient := createTestRecipient(t, svc, "13800000001")

	req := dto.ExternalConfirmRequest{MessageID: "msg-1", CareRecipientID: recipient.ID, Channel: "sms", Note: "ok"}
	first, err := svc.SubmitExternalConfirmation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replay {
		t.Fatal("first submission must not be a replay")
	}

	time.Sleep(20 * time.Millisecond)
	second, err := svc.SubmitExternalConfirmation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replay {
		t.Fatal("retry must report replay")
	}
	if second.Confirm.ID != first.Confirm.ID {
		t.Fatalf("retry created a new confirmation: first=%d second=%d", first.Confirm.ID, second.Confirm.ID)
	}
	if !second.Confirm.ConfirmedAt.Equal(first.Confirm.ConfirmedAt) {
		t.Fatalf("confirmed_at changed on replay: first=%v second=%v", first.Confirm.ConfirmedAt, second.Confirm.ConfirmedAt)
	}
	if !second.Recipient.LastConfirmedAt.Equal(*first.Recipient.LastConfirmedAt) {
		t.Fatal("last_confirmed_at must not change on replay")
	}

	var count int64
	if err := db.Model(&model.ReplyConfirm{}).Where("message_id = ?", "msg-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one confirmation record, got %d", count)
	}
}

func TestExternalConfirmConflictOnDifferentPayload(t *testing.T) {
	svc, db := newIdempotencyService(t)
	r1 := createTestRecipient(t, svc, "13800000002")
	r2 := createTestRecipient(t, svc, "13800000003")

	base := dto.ExternalConfirmRequest{MessageID: "msg-2", CareRecipientID: r1.ID, Channel: "sms", Note: "ok"}
	if _, err := svc.SubmitExternalConfirmation(context.Background(), base); err != nil {
		t.Fatal(err)
	}

	cases := []dto.ExternalConfirmRequest{
		{MessageID: "msg-2", CareRecipientID: r2.ID, Channel: "sms", Note: "ok"},
		{MessageID: "msg-2", CareRecipientID: r1.ID, Channel: "manual", Note: "ok"},
		{MessageID: "msg-2", CareRecipientID: r1.ID, Channel: "sms", Note: "changed"},
	}
	for i, req := range cases {
		_, err := svc.SubmitExternalConfirmation(context.Background(), req)
		if !errors.Is(err, repository.ErrIdempotencyConflict) {
			t.Fatalf("case %d: expected ErrIdempotencyConflict, got %v", i, err)
		}
	}

	var count int64
	if err := db.Model(&model.ReplyConfirm{}).Where("message_id = ?", "msg-2").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conflicting retries must not add records, got %d", count)
	}
}

func TestExternalConfirmConcurrentFirstWins(t *testing.T) {
	svc, db := newIdempotencyService(t)
	recipient := createTestRecipient(t, svc, "13800000004")

	const n = 8
	var wg sync.WaitGroup
	results := make([]*ConfirmationResult, n)
	errs := make([]error, n)
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			req := dto.ExternalConfirmRequest{MessageID: "msg-race", CareRecipientID: recipient.ID, Channel: "sms", Note: "ok"}
			results[i], errs[i] = svc.SubmitExternalConfirmation(context.Background(), req)
		}()
	}
	close(start)
	wg.Wait()

	firstCount, replayCount := 0, 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if results[i].Replay {
			replayCount++
		} else {
			firstCount++
		}
	}
	if firstCount != 1 || replayCount != n-1 {
		t.Fatalf("expected exactly 1 first and %d replays, got first=%d replay=%d", n-1, firstCount, replayCount)
	}

	var count int64
	if err := db.Model(&model.ReplyConfirm{}).Where("message_id = ?", "msg-race").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one record under concurrency, got %d", count)
	}
}

func TestConfirmWithoutMessageIDStillWorks(t *testing.T) {
	svc, db := newIdempotencyService(t)
	recipient := createTestRecipient(t, svc, "13800000005")

	if _, err := svc.Confirm(context.Background(), recipient.ID, dto.ConfirmRecipientRequest{Channel: "manual", Note: "call"}); err != nil {
		t.Fatal(err)
	}
	// Two unkeyed confirmations are independent (legacy behaviour preserved).
	if _, err := svc.Confirm(context.Background(), recipient.ID, dto.ConfirmRecipientRequest{Channel: "manual", Note: "call"}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.ReplyConfirm{}).Where("care_recipient_id = ?", recipient.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("legacy unkeyed confirmations should each insert a record, got %d", count)
	}
}

func TestConfirmFailureDoesNotAdvanceTimestamp(t *testing.T) {
	svc, db := newIdempotencyService(t)
	recipient := createTestRecipient(t, svc, "13800000006")

	first := dto.ExternalConfirmRequest{MessageID: "msg-fail", CareRecipientID: recipient.ID, Channel: "sms", Note: "ok"}
	if _, err := svc.SubmitExternalConfirmation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	var before model.CareRecipient
	if err := db.First(&before, recipient.ID).Error; err != nil {
		t.Fatal(err)
	}

	// With the reply-confirms table unavailable the confirm insert must fail
	// first inside the transaction; the recipient timestamp update must be
	// rolled back. No message_id is sent so the failure happens in the tx.
	if err := db.Migrator().DropTable(&model.ReplyConfirm{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	_, err := svc.Confirm(context.Background(), recipient.ID, dto.ConfirmRecipientRequest{Channel: "sms", Note: "ok"})
	if err == nil {
		t.Fatal("expected an error when the confirm table is unavailable")
	}
	var after model.CareRecipient
	if err := db.First(&after, recipient.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !after.LastConfirmedAt.Equal(*before.LastConfirmedAt) {
		t.Fatalf("last_confirmed_at advanced despite confirm write failure: before=%v after=%v", before.LastConfirmedAt, after.LastConfirmedAt)
	}
}
