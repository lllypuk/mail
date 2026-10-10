package mail_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"

	"github.com/lllypuk/mail"
)

// TestLetterEncode — письмо разбирается стандартным парсером: важно, поймёт ли его чужой почтовик.
func TestLetterEncode(t *testing.T) {
	t.Parallel()

	raw := letter().Encode("Паспорт объекта <noreply@dpp.example>")

	if !bytes.Contains(raw, []byte("\r\n\r\n")) {
		t.Fatal("в сообщении нет пустой строки между заголовками и телом")
	}

	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("разобрать сообщение: %v\n%s", err, raw)
	}

	from, err := netmail.ParseAddress(msg.Header.Get("From"))
	if err != nil {
		t.Fatalf("разобрать From %q: %v", msg.Header.Get("From"), err)
	}

	if from.Address != "noreply@dpp.example" || from.Name != "Паспорт объекта" {
		t.Fatalf("отправитель разобран как %q <%s>", from.Name, from.Address)
	}

	if got := msg.Header.Get("To"); got != letter().To {
		t.Fatalf("получатель %q, ожидался %q", got, letter().To)
	}

	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("декодировать тему: %v", err)
	}

	if subject != letter().Subject {
		t.Fatalf("тема %q, ожидалась %q", subject, letter().Subject)
	}

	if _, err = msg.Header.Date(); err != nil {
		t.Fatalf("разобрать дату %q: %v", msg.Header.Get("Date"), err)
	}

	if got := msg.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type %q", got)
	}

	if got := msg.Header.Get("Content-Transfer-Encoding"); got != "quoted-printable" {
		t.Fatalf("Content-Transfer-Encoding %q", got)
	}

	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatalf("прочитать тело: %v", err)
	}

	if string(body) != letter().Text {
		t.Fatalf("тело письма %q, ожидалось %q", body, letter().Text)
	}
}

func TestLetterEncodeASCIISubject(t *testing.T) {
	t.Parallel()

	raw := string(mail.Letter{To: "a@example.com", Subject: "Login", Text: "link"}.Encode("b@example.com"))

	if !strings.Contains(raw, "Subject: Login\r\n") {
		t.Fatalf("тема закодирована без нужды:\n%s", raw)
	}
}

// TestLetterEncodeUnchangedWithoutExtras — письмо без необязательных полей совпадает с письмом DPP.
func TestLetterEncodeUnchangedWithoutExtras(t *testing.T) {
	t.Parallel()

	raw := string(mail.Letter{To: "a@example.com", Subject: "Login", Text: "link"}.Encode("b@example.com"))

	var kept []string
	for line := range strings.SplitSeq(raw, "\r\n") {
		if !strings.HasPrefix(line, "Date: ") {
			kept = append(kept, line)
		}
	}

	want := "From: b@example.com\r\nTo: a@example.com\r\nSubject: Login\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n" +
		"Auto-Submitted: auto-generated\r\n\r\nlink"
	if got := strings.Join(kept, "\r\n"); got != want {
		t.Fatalf("письмо изменилось:\n%q\nожидалось\n%q", got, want)
	}
}

func TestLetterEncodeAttachments(t *testing.T) {
	t.Parallel()

	files := []mail.Attachment{
		{Name: "скриншот 1.jpg", MIME: "image/jpeg", Data: bytes.Repeat([]byte{0xFF, 0xD8, 0x01}, 200)},
		{Name: "photo.jpg", MIME: "image/jpeg", Data: []byte("jpeg")},
	}

	in := letter()
	in.ReplyTo = "pilot@example.com"
	in.Attachments = files

	msg, err := netmail.ReadMessage(bytes.NewReader(in.Encode("noreply@dpp.example")))
	if err != nil {
		t.Fatalf("разобрать сообщение: %v", err)
	}

	if got := msg.Header.Get("Reply-To"); got != in.ReplyTo {
		t.Errorf("Reply-To %q, ожидался %q", got, in.ReplyTo)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("Content-Type %q: %v", msg.Header.Get("Content-Type"), err)
	}

	reader := multipart.NewReader(msg.Body, params["boundary"])

	text, err := reader.NextPart()
	if err != nil {
		t.Fatalf("прочитать текст: %v", err)
	}

	if body, _ := io.ReadAll(text); string(body) != in.Text {
		t.Errorf("текст %q, ожидался %q", body, in.Text)
	}

	for _, want := range files {
		part, partErr := reader.NextPart()
		if partErr != nil {
			t.Fatalf("прочитать вложение %q: %v", want.Name, partErr)
		}

		checkAttachment(t, part, want)
	}

	if _, err = reader.NextPart(); !errors.Is(err, io.EOF) {
		t.Errorf("после вложений лишняя часть: %v", err)
	}
}

func TestLetterRejectsHeaderInjection(t *testing.T) {
	t.Parallel()

	for name, letter := range map[string]mail.Letter{
		"адресат": {To: "victim@example.com\r\nBcc: attacker@example.com", Subject: "Вход", Text: "ссылка"},
		"тема":    {To: "victim@example.com", Subject: "Login\r\nBcc: attacker@example.com", Text: "ссылка"},
		"отписка": {
			To:          "victim@example.com",
			Subject:     "Вход",
			Text:        "ссылка",
			Unsubscribe: "https://dpp.example/unsubscribe/x\r\nBcc: attacker@example.com",
		},
		"ответ": {
			To:      "victim@example.com",
			Subject: "Отзыв",
			Text:    "текст",
			ReplyTo: "pilot@example.com\r\nBcc: attacker@example.com",
		},
		"Message-ID": {
			To:        "victim@example.com",
			Subject:   "Вход",
			Text:      "ссылка",
			MessageID: "<a@dpp.example>\r\nBcc: attacker@example.com",
		},
		"имя вложения": {
			To:          "victim@example.com",
			Subject:     "Отзыв",
			Text:        "текст",
			Attachments: []mail.Attachment{{Name: "a\".jpg", MIME: "image/jpeg"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := letter.Validate(); !errors.Is(err, mail.ErrHeaderInjection) {
				t.Fatalf("ошибка %v, ожидалась ErrHeaderInjection", err)
			}
		})
	}
}

func TestLetterRejectsInvalidAddress(t *testing.T) {
	t.Parallel()

	for name, letter := range map[string]mail.Letter{
		"пустой To":          {Subject: "Вход", Text: "ссылка"},
		"To без @":           {To: "hozyain", Subject: "Вход", Text: "ссылка"},
		"два адресата":       {To: "a@example.com, b@example.com", Subject: "Вход", Text: "ссылка"},
		"Message-ID без <>":  {To: "a@example.com", MessageID: "id@dpp.example"},
		"Message-ID без @":   {To: "a@example.com", MessageID: "<id>"},
		"Message-ID пустой":  {To: "a@example.com", MessageID: "<@dpp.example>"},
		"Message-ID пробелы": {To: "a@example.com", MessageID: "<a b@dpp.example>"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := letter.Validate(); !errors.Is(err, mail.ErrInvalidAddress) {
				t.Fatalf("ошибка %v, ожидалась ErrInvalidAddress", err)
			}
		})
	}

	if err := letter().Validate(); err != nil {
		t.Fatalf("корректное письмо отклонено: %v", err)
	}
}

func TestLetterEncodeNamedRecipient(t *testing.T) {
	t.Parallel()

	in := letter()
	in.To = "Иван Хозяин <hozyain@example.com>"

	if err := in.Validate(); err != nil {
		t.Fatalf("письмо отклонено: %v", err)
	}

	raw := in.Encode("noreply@dpp.example")

	header := msgHeader(t, raw, "To")
	if !strings.Contains(header, "=?utf-8?") {
		t.Errorf("имя адресата не закодировано по RFC 2047: %q", header)
	}

	to, err := netmail.ParseAddress(header)
	if err != nil {
		t.Fatalf("разобрать To %q: %v", header, err)
	}

	if to.Name != "Иван Хозяин" || to.Address != "hozyain@example.com" {
		t.Errorf("адресат разобран как %q <%s>", to.Name, to.Address)
	}
}

func TestLetterEncodeMessageID(t *testing.T) {
	t.Parallel()

	in := letter()
	if got := msgHeader(t, in.Encode("noreply@dpp.example"), "Message-ID"); got != "" {
		t.Errorf("у письма без MessageID заголовок %q", got)
	}

	in.MessageID = "<0d4f1c@dpp.example>"

	if err := in.Validate(); err != nil {
		t.Fatalf("письмо отклонено: %v", err)
	}

	if got := msgHeader(t, in.Encode("noreply@dpp.example"), "Message-ID"); got != in.MessageID {
		t.Errorf("Message-ID %q, ожидался %q", got, in.MessageID)
	}
}

func TestLetterEncodeUnsubscribe(t *testing.T) {
	t.Parallel()

	const link = "https://dpp.example/unsubscribe/6RRy4kQeaVXCHOoqK6nDVFHkFPUFTx5rz6XkVjuVlxc"

	plain := letter()
	if got := msgHeader(t, plain.Encode("noreply@dpp.example"), "List-Unsubscribe"); got != "" {
		t.Errorf("у письма без отписки заголовок %q", got)
	}

	plain.Unsubscribe = link
	raw := plain.Encode("noreply@dpp.example")

	if got := msgHeader(t, raw, "List-Unsubscribe"); got != "<"+link+">" {
		t.Errorf("List-Unsubscribe %q, ожидался адрес в угловых скобках", got)
	}

	if got := msgHeader(t, raw, "List-Unsubscribe-Post"); got != "List-Unsubscribe=One-Click" {
		t.Errorf("List-Unsubscribe-Post %q — клиент не покажет свою кнопку", got)
	}
}

func checkAttachment(t *testing.T, part *multipart.Part, want mail.Attachment) {
	t.Helper()

	mediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if part.FileName() != want.Name || mediaType != want.MIME {
		t.Errorf("вложение %q типа %q, ожидалось %q", part.FileName(), mediaType, want.Name)
	}

	raw, _ := io.ReadAll(part)
	for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\r\n"), "\r\n") {
		if len(line) > 76 {
			t.Fatalf("строка base64 длиной %d", len(line))
		}
	}

	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(raw), "\r\n", ""))
	if err != nil || !bytes.Equal(data, want.Data) {
		t.Errorf("байты вложения %q не совпали: %v", want.Name, err)
	}
}

func msgHeader(t *testing.T, raw []byte, name string) string {
	t.Helper()

	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("разобрать сообщение: %v\n%s", err, raw)
	}

	return msg.Header.Get(name)
}

func letter() mail.Letter {
	return mail.Letter{
		To:      "hozyain@example.com",
		Subject: "Вход в паспорт объекта",
		Text:    "Ссылка действует 15 минут:\r\nhttps://dpp.example/login/abc-123\r\n",
	}
}
