package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/blueship581/gbcarenotify/internal/dto"
	"github.com/blueship581/gbcarenotify/internal/model"
	"github.com/blueship581/gbcarenotify/internal/repository"
	"github.com/blueship581/gbcarenotify/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func setupConfirmRouter(t *testing.T) (*gin.Engine, uint) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	recipientRepo := repository.NewRecipientRepository(db)
	item := &model.CareRecipient{Name: "王阿姨", Phone: "13800138000", CareFrequency: "daily", CareStartAt: time.Now().UTC().Add(-time.Hour), Status: "Active"}
	if err := recipientRepo.Create(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recipientService := service.NewRecipientService(db, recipientRepo, repository.NewReplyConfirmRepository(db), logger)
	engine := gin.New()
	engine.POST("/api/v1/recipients/:id/confirmations", NewRecipientHandler(NewBaseHandler(), recipientService).Confirm)
	return engine, item.ID
}

func postConfirm(t *testing.T, engine *gin.Engine, id uint, body string) (int, dto.APIResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/recipients/"+strconv.Itoa(int(id))+"/confirmations", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	var resp dto.APIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return recorder.Code, resp
}

func TestConfirmHandlerIdempotency(t *testing.T) {
	engine, id := setupConfirmRouter(t)
	payload := `{"message_id":"msg-100","channel":"sms","note":"回复平安"}`
	code, first := postConfirm(t, engine, id, payload)
	if code != http.StatusOK {
		t.Fatalf("first confirm code=%d, want 200", code)
	}
	code, replayed := postConfirm(t, engine, id, payload)
	if code != http.StatusOK {
		t.Fatalf("replay code=%d, want 200", code)
	}
	firstData, _ := json.Marshal(first.Data)
	replayedData, _ := json.Marshal(replayed.Data)
	if string(firstData) != string(replayedData) {
		t.Fatalf("replay data=%s, want first result %s", replayedData, firstData)
	}
	code, conflict := postConfirm(t, engine, id, `{"message_id":"msg-100","channel":"sms","note":"改口"}`)
	if code != http.StatusConflict || conflict.Code != 40901 {
		t.Fatalf("conflict code=%d body=%+v, want 409/40901", code, conflict)
	}
	if code, _ = postConfirm(t, engine, id, `{"channel":"manual","note":"电话确认"}`); code != http.StatusOK {
		t.Fatalf("legacy confirm code=%d, want 200", code)
	}
}
