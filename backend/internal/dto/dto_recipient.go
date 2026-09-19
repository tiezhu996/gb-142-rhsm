package dto

type CreateRecipientRequest struct {
	Name          string `json:"name" validate:"required,max=100"`
	Phone         string `json:"phone" validate:"required,min=6,max=32"`
	CareFrequency string `json:"care_frequency" validate:"required,oneof=daily weekly monthly"`
	CareStartAt   string `json:"care_start_at" validate:"required,datetime=2006-01-02T15:04:05Z07:00"`
	Status        string `json:"status" validate:"omitempty,oneof=Active Paused"`
}
type UpdateRecipientRequest struct {
	Name          string `json:"name" validate:"required,max=100"`
	Phone         string `json:"phone" validate:"required,min=6,max=32"`
	CareFrequency string `json:"care_frequency" validate:"required,oneof=daily weekly monthly"`
	CareStartAt   string `json:"care_start_at" validate:"required,datetime=2006-01-02T15:04:05Z07:00"`
	Status        string `json:"status" validate:"required,oneof=Active Paused"`
}
type ConfirmRecipientRequest struct {
	MessageID string `json:"message_id" validate:"omitempty,max=128"`
	Channel   string `json:"channel" validate:"omitempty,oneof=sms manual webhook"`
	Note      string `json:"note" validate:"omitempty,max=500"`
}

// ExternalConfirmRequest is used by external systems submitting confirmations.
// MessageID is the replayable idempotency key: retries with the same value
// return the first stored result instead of creating another record.
type ExternalConfirmRequest struct {
	MessageID       string `json:"message_id" validate:"required,max=128"`
	CareRecipientID uint   `json:"care_recipient_id" validate:"required"`
	Channel         string `json:"channel" validate:"omitempty,oneof=sms manual webhook"`
	Note            string `json:"note" validate:"omitempty,max=500"`
}
type CreateSubscriptionRequest struct {
	FamilyName  string `json:"family_name" validate:"required,max=100"`
	FamilyPhone string `json:"family_phone" validate:"required,min=6,max=32"`
	Active      *bool  `json:"active"`
}
