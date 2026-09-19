package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/blueship581/gbcarenotify/internal/constants"
	"github.com/blueship581/gbcarenotify/internal/dto"
	"github.com/blueship581/gbcarenotify/internal/model"
	"github.com/blueship581/gbcarenotify/internal/repository"
	"gorm.io/gorm"
	"log/slog"
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

// ConfirmationResult is returned for an idempotent confirmation submission.
// Replay is true when the request repeated an existing message_id.
type ConfirmationResult struct {
	Recipient *model.CareRecipient `json:"recipient"`
	Confirm   *model.ReplyConfirm  `json:"confirmation"`
	Replay    bool                 `json:"replay"`
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

// Confirm records a confirmation from the management API. It keeps its
// original behaviour for requests without a message_id; when a message_id is
// supplied it is handled as an idempotent, replayable submission.
func (s *RecipientService) Confirm(ctx context.Context, id uint, req dto.ConfirmRecipientRequest) (*model.CareRecipient, error) {
	channel := req.Channel
	if channel == "" {
		channel = "manual"
	}
	var messageID *string
	if req.MessageID != "" {
		messageID = &req.MessageID
	}
	result, err := s.submit(ctx, id, messageID, channel, req.Note)
	if err != nil {
		return nil, err
	}
	return result.Recipient, nil
}

// SubmitExternalConfirmation records a confirmation submitted by an external
// system with a replayable message_id. The same message_id retried returns the
// first stored result (Replay=true); a reused message_id with different
// recipient/channel/note returns repository.ErrIdempotencyConflict.
func (s *RecipientService) SubmitExternalConfirmation(ctx context.Context, req dto.ExternalConfirmRequest) (*ConfirmationResult, error) {
	channel := req.Channel
	if channel == "" {
		channel = "webhook"
	}
	return s.submit(ctx, req.CareRecipientID, &req.MessageID, channel, req.Note)
}

func (s *RecipientService) submit(ctx context.Context, recipientID uint, messageID *string, channel, note string) (*ConfirmationResult, error) {
	// Fail fast on an unknown recipient; pre-checks for existing keys are also
	// cheap here and avoid opening a transaction on plain replays.
	if _, err := s.repo.Get(ctx, recipientID); err != nil {
		return nil, err
	}
	if messageID != nil {
		existing, err := s.confirms.GetByMessageID(ctx, *messageID)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		if existing != nil {
			return s.replayResult(ctx, existing, recipientID, channel, note)
		}
	}

	record := &model.ReplyConfirm{
		MessageID:       messageID,
		CareRecipientID: recipientID,
		Channel:         channel,
		Note:            note,
	}
	// The reply-confirm row is written first; the recipient timestamp is only
	// advanced afterwards, inside the same transaction, so a failed confirm
	// insert (including losing a concurrent first-insert race) never advances
	// last_confirmed_at and never leaves a second record.
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txConfirms := repository.NewReplyConfirmRepository(tx)
		txRecipients := repository.NewRecipientRepository(tx)
		now := time.Now().UTC()
		record.ConfirmedAt = now
		if err := txConfirms.Create(ctx, record); err != nil {
			return err
		}
		if err := txRecipients.TouchConfirmedAt(ctx, recipientID, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		// A concurrent first submission won the unique-index race.
		if errors.Is(err, repository.ErrDuplicateKey) && messageID != nil {
			existing, getErr := s.confirms.GetByMessageID(ctx, *messageID)
			if getErr != nil {
				return nil, getErr
			}
			return s.replayResult(ctx, existing, recipientID, channel, note)
		}
		return nil, err
	}
	recipient, err := s.repo.Get(ctx, recipientID)
	if err != nil {
		return nil, err
	}
	s.logger.Info("recipient confirmed", "recipient_id", recipientID, "channel", channel, "message_id", messageID)
	return &ConfirmationResult{Recipient: recipient, Confirm: record, Replay: false}, nil
}

// replayResult validates a retried submission against the stored confirmation
// and returns the first stored result without mutating anything.
func (s *RecipientService) replayResult(ctx context.Context, existing *model.ReplyConfirm, recipientID uint, channel, note string) (*ConfirmationResult, error) {
	if existing.CareRecipientID != recipientID || existing.Channel != channel || existing.Note != note {
		return nil, fmt.Errorf("message_id %q already used: %w", *existing.MessageID, repository.ErrIdempotencyConflict)
	}
	recipient, err := s.repo.Get(ctx, existing.CareRecipientID)
	if err != nil {
		return nil, err
	}
	return &ConfirmationResult{Recipient: recipient, Confirm: existing, Replay: true}, nil
}

func (s *RecipientService) MarkGreeted(ctx context.Context, item *model.CareRecipient, now time.Time) error {
	if item == nil {
		return errors.New("recipient is nil")
	}
	item.LastGreetingAt = &now
	return s.repo.Update(ctx, item)
}
