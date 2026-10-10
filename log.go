package mail

import (
	"context"
	"log/slog"
	"strconv"
)

// Log пишет письмо в лог вместо отправки — режим разработки без почтового ящика.
type Log struct {
	logger *slog.Logger
}

// NewLog создаёт Log; nil — slog.Default().
func NewLog(logger *slog.Logger) *Log {
	if logger == nil {
		logger = slog.Default()
	}

	return &Log{logger: logger}
}

// Send пишет письмо целиком, с текстом: ради ссылки входа в нём режим и существует. От вложений — имя и размер.
func (m *Log) Send(ctx context.Context, letter Letter) error {
	files := make([]string, 0, len(letter.Attachments))
	for _, a := range letter.Attachments {
		files = append(files, a.Name+" ("+strconv.Itoa(len(a.Data))+" байт)")
	}

	m.logger.InfoContext(ctx, "письмо не отправлено, режим разработки",
		slog.String("to", letter.To),
		slog.String("reply_to", letter.ReplyTo),
		slog.String("subject", letter.Subject),
		slog.String("text", letter.Text),
		slog.Any("attachments", files),
	)

	return nil
}
