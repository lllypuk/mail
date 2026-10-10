package mail

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"net/textproto"
	"strings"
	"time"
)

var (
	// ErrHeaderInjection — перевод строки в заголовочном поле письма или кавычка в имени вложения.
	ErrHeaderInjection = errors.New("перевод строки в заголовке письма")
	// ErrInvalidAddress — адресат или Message-ID не разбираются.
	ErrInvalidAddress = errors.New("неразборчивый адрес письма")
)

const (
	crlf = "\r\n"
	// base64Line — длина строки base64 в части письма (RFC 2045 §6.8).
	base64Line = 76
)

// Letter — письмо; отправителя знает SMTP из конфигурации, а не письмо.
type Letter struct {
	// To — адрес RFC 5322, с именем или без.
	To      string
	Subject string
	Text    string
	// Unsubscribe — адрес отписки одним нажатием (RFC 8058); пусто — заголовка нет.
	Unsubscribe string
	// ReplyTo — куда уходит «Ответить»; пусто — отправителю.
	ReplyTo string
	// MessageID — значение вида <id@host>; пусто — заголовок ставит сервер.
	MessageID   string
	Attachments []Attachment
}

// Attachment — файл письма; с ним письмо уходит multipart/mixed.
type Attachment struct {
	Name string
	MIME string
	Data []byte
}

// Validate проверяет, что письмо собирается в сообщение без чужих заголовков.
// Тело не проверяется: в quoted-printable перевод строки заголовком не станет.
func (l Letter) Validate() error {
	for _, field := range []struct{ name, value string }{
		{name: "To", value: l.To},
		// Латинская тема уходит без RFC 2047, вместе с переводами строк.
		{name: "Subject", value: l.Subject},
		{name: "List-Unsubscribe", value: l.Unsubscribe},
		{name: "Reply-To", value: l.ReplyTo},
		{name: "Message-ID", value: l.MessageID},
	} {
		if strings.ContainsAny(field.value, "\r\n") {
			return fmt.Errorf("%w: %s", ErrHeaderInjection, field.name)
		}
	}

	for _, a := range l.Attachments {
		if strings.ContainsAny(a.Name, "\r\n\"") || strings.ContainsAny(a.MIME, "\r\n") {
			return fmt.Errorf("%w: вложение %q", ErrHeaderInjection, a.Name)
		}
	}

	if _, err := l.recipient(); err != nil {
		return err
	}

	if l.MessageID != "" && !validMessageID(l.MessageID) {
		return fmt.Errorf("%w: Message-ID %q", ErrInvalidAddress, l.MessageID)
	}

	return nil
}

// Encode собирает письмо в сообщение RFC 5322: тема по RFC 2047, тело quoted-printable в UTF-8.
// Письмо без Validate может унести в заголовок неразобранный To как есть.
func (l Letter) Encode(from string) []byte {
	var buf bytes.Buffer

	writeHeader(&buf, "From", from)
	writeHeader(&buf, "To", l.headerTo())

	if l.ReplyTo != "" {
		writeHeader(&buf, "Reply-To", l.ReplyTo)
	}

	writeHeader(&buf, "Subject", mime.QEncoding.Encode("utf-8", l.Subject))
	writeHeader(&buf, "Date", time.Now().Format(time.RFC1123Z))

	if l.MessageID != "" {
		writeHeader(&buf, "Message-ID", l.MessageID)
	}

	writeHeader(&buf, "MIME-Version", "1.0")

	var parts *multipart.Writer
	if len(l.Attachments) > 0 {
		parts = multipart.NewWriter(&buf)
		writeHeader(&buf, "Content-Type",
			mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": parts.Boundary()}))
	} else {
		writeHeader(&buf, "Content-Type", "text/plain; charset=utf-8")
		writeHeader(&buf, "Content-Transfer-Encoding", "quoted-printable")
	}

	writeHeader(&buf, "Auto-Submitted", "auto-generated")

	// RFC 8058: без второго заголовка клиент не покажет кнопку отписки.
	if l.Unsubscribe != "" {
		writeHeader(&buf, "List-Unsubscribe", "<"+l.Unsubscribe+">")
		writeHeader(&buf, "List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	}

	buf.WriteString(crlf)

	if parts == nil {
		writeText(&buf, l.Text)

		return buf.Bytes()
	}

	// Запись в буфер в памяти отказывает только при нехватке памяти.
	text, _ := parts.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	writeText(text, l.Text)

	for _, a := range l.Attachments {
		part, _ := parts.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(a.MIME, map[string]string{"name": a.Name})},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": a.Name})},
			"Content-Transfer-Encoding": {"base64"},
		})
		writeBase64(part, a.Data)
	}

	_ = parts.Close()

	return buf.Bytes()
}

// recipient разбирает To; голый Address уходит в RCPT TO.
func (l Letter) recipient() (*netmail.Address, error) {
	addr, err := netmail.ParseAddress(l.To)
	if err != nil {
		return nil, fmt.Errorf("%w: To %q: %w", ErrInvalidAddress, l.To, err)
	}

	return addr, nil
}

// headerTo — To для заголовка: с именем по RFC 2047, без имени голый адрес без <>.
func (l Letter) headerTo() string {
	addr, err := l.recipient()
	if err != nil {
		return l.To
	}

	if addr.Name == "" {
		return addr.Address
	}

	return addr.String()
}

func validMessageID(id string) bool {
	inner, ok := strings.CutPrefix(id, "<")
	if !ok {
		return false
	}

	inner, ok = strings.CutSuffix(inner, ">")
	if !ok || strings.ContainsAny(inner, " \t<>") {
		return false
	}

	local, domain, ok := strings.Cut(inner, "@")

	return ok && local != "" && domain != ""
}

func writeText(w io.Writer, text string) {
	qp := quotedprintable.NewWriter(w)
	_, _ = qp.Write([]byte(text))
	_ = qp.Close()
}

func writeBase64(w io.Writer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > base64Line {
		_, _ = io.WriteString(w, encoded[:base64Line]+crlf)
		encoded = encoded[base64Line:]
	}

	_, _ = io.WriteString(w, encoded+crlf)
}

func writeHeader(buf *bytes.Buffer, name, value string) {
	_, _ = fmt.Fprintf(buf, "%s: %s%s", name, value, crlf)
}
