package repository

import (
	"context"
	"errors"
	"fmt"
	"github.com/blueship581/gbcarenotify/internal/model"
	"gorm.io/gorm"
)

// ErrIdempotencyConflict means a confirmation record already exists for the
// same message_id but with different recipient, channel or note.
var ErrIdempotencyConflict = errors.New("idempotency conflict")

type ReplyConfirmRepository struct{ db *gorm.DB }

func NewReplyConfirmRepository(db *gorm.DB) *ReplyConfirmRepository {
	return &ReplyConfirmRepository{db: db}
}
func (r *ReplyConfirmRepository) Create(ctx context.Context, item *model.ReplyConfirm) error {
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		if IsDuplicateKeyErr(err) {
			return fmt.Errorf("create reply confirm: %w: %w", ErrDuplicateKey, err)
		}
		return fmt.Errorf("create reply confirm: %w", err)
	}
	return nil
}

// GetByMessageID returns the confirmation stored for an idempotency key.
func (r *ReplyConfirmRepository) GetByMessageID(ctx context.Context, messageID string) (*model.ReplyConfirm, error) {
	var item model.ReplyConfirm
	err := r.db.WithContext(ctx).Where("message_id = ?", messageID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get reply confirm by message id: %w", err)
	}
	return &item, nil
}
