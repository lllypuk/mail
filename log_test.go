package mail_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/lllypuk/mail"
)

// TestLogWritesEverything — в записи всё письмо, от вложений только имя и размер.
func TestLogWritesEverything(t *testing.T) {
	t.Parallel()

	logs := &bytes.Buffer{}
	mailer := mail.NewLog(slog.New(slog.NewJSONHandler(logs, nil)))

	in := letter()
	in.ReplyTo = "pilot@example.com"
	in.Attachments = []mail.Attachment{{Name: "a.jpg", MIME: "image/jpeg", Data: []byte("jpeg")}}

	if err := mailer.Send(t.Context(), in); err != nil {
		t.Fatalf("отправить письмо: %v", err)
	}

	record := map[string]any{}
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("разобрать запись лога %q: %v", logs, err)
	}

	for key, want := range map[string]string{
		"level":    "INFO",
		"msg":      "письмо не отправлено, режим разработки",
		"to":       letter().To,
		"subject":  letter().Subject,
		"text":     letter().Text,
		"reply_to": "pilot@example.com",
	} {
		if got, _ := record[key].(string); got != want {
			t.Fatalf("в логе %s=%q, ожидалось %q", key, got, want)
		}
	}

	if files, _ := record["attachments"].([]any); len(files) != 1 || files[0] != "a.jpg (4 байт)" {
		t.Fatalf("в логе вложения %v, ожидались имя и размер", record["attachments"])
	}
}

// TestNewLogNil — fitness-app собирает Log без логгера, сборка и Send не паникуют.
func TestNewLogNil(t *testing.T) {
	t.Parallel()

	if err := mail.NewLog(nil).Send(t.Context(), letter()); err != nil {
		t.Fatalf("отправить письмо: %v", err)
	}
}
