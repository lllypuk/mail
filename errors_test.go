package mail_test

import (
	"errors"
	"net/textproto"
	"strings"
	"testing"

	"github.com/lllypuk/mail"
	"github.com/lllypuk/mail/internal/smtptest"
)

// requireNoAddress — ни одного адреса входа в тексте ошибки ни в каком регистре.
func requireNoAddress(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	text := strings.ToLower(err.Error())
	for _, addr := range []string{"ivan@example.com", "petr@example.com", "noreply@dpp.example", "robot@dpp.example"} {
		if strings.Contains(text, addr) {
			t.Fatalf("адрес %s в тексте ошибки: %v", addr, err)
		}
	}
}

func TestErrorsWithoutAddressOffline(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		letter mail.Letter
		want   error
	}{
		{name: "два адресата", letter: mail.Letter{To: "ivan@example.com, petr@example.com"}, want: mail.ErrInvalidAddress},
		{name: "незакрытая скобка", letter: mail.Letter{To: "Иван <ivan@example.com"}, want: mail.ErrInvalidAddress},
		{name: "local-part в кавычках", letter: mail.Letter{To: `"ivan petr"@example.com`}, want: mail.ErrInvalidAddress},
		{
			name:   "Message-ID без скобок",
			letter: mail.Letter{To: "ivan@example.com", MessageID: "petr@example.com"},
			want:   mail.ErrInvalidAddress,
		},
		{
			name:   "перевод строки в To",
			letter: mail.Letter{To: "ivan@example.com\r\nBcc: petr@example.com"},
			want:   mail.ErrHeaderInjection,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.letter.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("ошибка %v, ожидалась %v", err, tc.want)
			}

			requireNoAddress(t, err)
		})
	}

	for _, from := range []string{"noreply@dpp.example, petr@example.com", "Робот <noreply@dpp.example", `"no reply"@dpp.example`} {
		cfg := validConfig()
		cfg.From = from

		_, err := mail.NewSMTP(cfg)
		if !errors.Is(err, mail.ErrInvalidFrom) {
			t.Fatalf("From %q: ошибка %v, ожидалась ErrInvalidFrom", from, err)
		}

		requireNoAddress(t, err)
	}
}

func TestErrorsWithoutAddressFromServer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		opts   smtptest.Options
		phase  mail.Phase
		code   int
		reason string
	}{
		{
			name:   "AUTH",
			opts:   smtptest.Options{AuthReply: "535 5.7.8 robot@dpp.example: authentication failed"},
			phase:  mail.PhaseAuth,
			code:   535,
			reason: "authentication failed",
		},
		{
			name:   "MAIL",
			opts:   smtptest.Options{MailReply: "553 5.1.8 <noreply@dpp.example>: Sender address rejected"},
			phase:  mail.PhaseEnvelope,
			code:   553,
			reason: "Sender address rejected",
		},
		{
			name:   "RCPT в скобках",
			opts:   smtptest.Options{RcptReply: "550 5.1.1 <ivan@example.com>: Recipient address rejected"},
			phase:  mail.PhaseEnvelope,
			code:   550,
			reason: "Recipient address rejected",
		},
		{
			name:   "RCPT голым адресом в другом регистре",
			opts:   smtptest.Options{RcptReply: "550 5.1.1 IVAN@EXAMPLE.COM... User unknown"},
			phase:  mail.PhaseEnvelope,
			code:   550,
			reason: "User unknown",
		},
		{
			name:   "точка",
			opts:   smtptest.Options{DataReply: "554 5.7.1 message from noreply@dpp.example to ivan@example.com: spam"},
			phase:  mail.PhaseData,
			code:   554,
			reason: "spam",
		},
		{
			name:   "строка без кода",
			opts:   smtptest.Options{RcptReply: "5.1.1 <ivan@example.com> unknown"},
			phase:  mail.PhaseEnvelope,
			reason: "unknown",
		},
		{
			name:   "двоеточие после кода",
			opts:   smtptest.Options{RcptReply: "550:ivan@example.com rejected"},
			phase:  mail.PhaseEnvelope,
			reason: "rejected",
		},
		{
			name:   "скобка вплотную к коду",
			opts:   smtptest.Options{RcptReply: "550<ivan@example.com>: rejected"},
			phase:  mail.PhaseEnvelope,
			reason: "rejected",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.opts.Mode = smtptest.StartTLS
			srv := smtptest.Start(t, tc.opts)

			sender, err := mail.NewSMTP(mail.Config{
				Host:     smtptest.Host,
				Port:     srv.Port(),
				Username: "robot@dpp.example",
				Password: "secret",
				From:     "Паспорт объекта <noreply@dpp.example>",
				RootCAs:  srv.RootCAs(),
			})
			if err != nil {
				t.Fatalf("NewSMTP: %v", err)
			}

			err = sender.Send(t.Context(), testLetter())
			sendErr := requireSendError(t, err, tc.phase, mail.OutcomeRejected)

			if sendErr.Code != tc.code || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("код %d, текст %q; ожидались %d и причина %q", sendErr.Code, err, tc.code, tc.reason)
			}

			_, replied := errors.AsType[*textproto.Error](err)
			_, broken := errors.AsType[textproto.ProtocolError](err)

			if replied != (tc.code != 0) || broken != (tc.code == 0) {
				t.Fatalf("исходная ошибка сервера не видна через errors.As: %#v", sendErr.Err)
			}

			requireNoAddress(t, err)
		})
	}
}

func TestSendErrorNilCause(t *testing.T) {
	err := &mail.SendError{Phase: mail.PhaseEnvelope, Code: 550}
	if got := err.Error(); !strings.Contains(got, "<nil>") {
		t.Fatalf("Error() = %q, want <nil> cause", got)
	}
}
