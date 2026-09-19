package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/blueship581/gbcarenotify/internal/model"
	"gorm.io/gorm"
)

type ReplyConfirmRepository struct{ db *gorm.DB }

func NewReplyConfirmRepository(db *gorm.DB) *ReplyConfirmRepository {
	return &ReplyConfirmRepository{db: db}
}
func (r *ReplyConfirmRepository) Create(ctx context.Context, item *model.ReplyConfirm) error {
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		if isDuplicateKeyError(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("create reply confirm: %w", err)
	}
	return nil
}
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

// isDuplicateKeyError 识别 MySQL(1062 Duplicate entry)与 SQLite(UNIQUE constraint)的唯一键冲突。
func isDuplicateKeyError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") || strings.Contains(msg, "UNIQUE constraint failed")
}
