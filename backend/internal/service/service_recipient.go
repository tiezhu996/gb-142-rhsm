package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/blueship581/gbcarenotify/internal/constants"
	"github.com/blueship581/gbcarenotify/internal/dto"
	"github.com/blueship581/gbcarenotify/internal/model"
	"github.com/blueship581/gbcarenotify/internal/repository"
	"gorm.io/gorm"
	"log/slog"
	"time"
)

type RecipientService struct {
	db       *gorm.DB
	repo     *repository.RecipientRepository
	confirms *repository.ReplyConfirmRepository
	logger   *slog.Logger
}

func NewRecipientService(db *gorm.DB, repo *repository.RecipientRepository, confirms *repository.ReplyConfirmRepository, logger *slog.Logger) *RecipientService {
	return &RecipientService{db: db, repo: repo, confirms: confirms, logger: logger}
}
func (s *RecipientService) Create(ctx context.Context, req dto.CreateRecipientRequest) (*model.CareRecipient, error) {
	start, err := time.Parse(time.RFC3339, req.CareStartAt)
	if err != nil {
		return nil, fmt.Errorf("parse care start time: %w", err)
	}
	status := req.Status
	if status == "" {
		status = constants.RecipientStatusActive
	}
	item := &model.CareRecipient{Name: req.Name, Phone: req.Phone, CareFrequency: req.CareFrequency, CareStartAt: start, Status: status}
	if err = s.repo.Create(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}
func (s *RecipientService) List(ctx context.Context, page, pageSize int) ([]model.CareRecipient, int64, error) {
	return s.repo.List(ctx, page, pageSize)
}
func (s *RecipientService) Get(ctx context.Context, id uint) (*model.CareRecipient, error) {
	return s.repo.Get(ctx, id)
}
func (s *RecipientService) Update(ctx context.Context, id uint, req dto.UpdateRecipientRequest) (*model.CareRecipient, error) {
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	start, err := time.Parse(time.RFC3339, req.CareStartAt)
	if err != nil {
		return nil, fmt.Errorf("parse care start time: %w", err)
	}
	item.Name = req.Name
	item.Phone = req.Phone
	item.CareFrequency = req.CareFrequency
	item.CareStartAt = start
	item.Status = req.Status
	if err := s.repo.Update(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}
func (s *RecipientService) Delete(ctx context.Context, id uint) error { return s.repo.Delete(ctx, id) }
func (s *RecipientService) Confirm(ctx context.Context, id uint, req dto.ConfirmRecipientRequest) (*model.CareRecipient, error) {
	channel := req.Channel
	if channel == "" {
		channel = "manual"
	}
	if req.MessageID != "" {
		// 幂等重放：同一 message_id 直接返回首次结果，负载不一致则冲突
		item, err := s.replay(ctx, id, req.MessageID, channel, req.Note)
		if err != nil || item != nil {
			return item, err
		}
	}
	// 对象不存在时直接返回未找到
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	confirm := &model.ReplyConfirm{CareRecipientID: id, Channel: channel, Note: req.Note, ConfirmedAt: now}
	if req.MessageID != "" {
		confirm.MessageID = &req.MessageID
	}
	// 先写确认记录再推进对象时间，任一失败整体回滚，对象时间不被推进
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := repository.NewReplyConfirmRepository(tx).Create(ctx, confirm); err != nil {
			return err
		}
		return repository.NewRecipientRepository(tx).UpdateLastConfirmedAt(ctx, id, now)
	})
	if err != nil {
		if req.MessageID != "" && errors.Is(err, repository.ErrDuplicate) {
			// 并发首次提交：唯一索引保证只有一条写入成功，按重放处理
			if replayed, rerr := s.replay(ctx, id, req.MessageID, channel, req.Note); rerr != nil || replayed != nil {
				return replayed, rerr
			}
		}
		return nil, err
	}
	// 返回提交后的最新状态，与幂等重放的响应保持一致
	stored, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.logger.Info("recipient confirmed", "recipient_id", id, "channel", channel)
	return stored, nil
}

// replay 查询 message_id 对应的首条确认：负载一致返回首次结果，不一致返回冲突，不存在返回 (nil, nil)。
func (s *RecipientService) replay(ctx context.Context, id uint, messageID, channel, note string) (*model.CareRecipient, error) {
	existing, err := s.confirms.GetByMessageID(ctx, messageID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existing.CareRecipientID != id || existing.Channel != channel || existing.Note != note {
		return nil, repository.ErrConflict
	}
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	confirmedAt := existing.ConfirmedAt
	item.LastConfirmedAt = &confirmedAt
	return item, nil
}
func (s *RecipientService) MarkGreeted(ctx context.Context, item *model.CareRecipient, now time.Time) error {
	if item == nil {
		return errors.New("recipient is nil")
	}
	item.LastGreetingAt = &now
	return s.repo.Update(ctx, item)
}
