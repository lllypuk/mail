package mail_test

import (
	"context"
	"errors"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/lllypuk/mail"
	"github.com/lllypuk/mail/internal/smtptest"
)

func validConfig() mail.Config {
	return mail.Config{
		Host: "smtp.example",
		Port: 587,
		From: "Паспорт объекта <noreply@dpp.example>",
	}
}

func TestNewSMTPRejectsConfig(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		mutate func(*mail.Config)
		want   error
	}{
		{name: "пустой From", mutate: func(c *mail.Config) { c.From = "" }, want: mail.ErrInvalidFrom},
		{name: "неразборчивый From", mutate: func(c *mail.Config) { c.From = "noreply" }, want: mail.ErrInvalidFrom},
		{name: "пустой Host", mutate: func(c *mail.Config) { c.Host = "" }, want: mail.ErrConfig},
		{name: "порт 0", mutate: func(c *mail.Config) { c.Port = 0 }, want: mail.ErrConfig},
		{name: "порт 65536", mutate: func(c *mail.Config) { c.Port = 65536 }, want: mail.ErrConfig},
		{name: "неизвестный TLSMode", mutate: func(c *mail.Config) { c.TLS = 7 }, want: mail.ErrConfig},
		{name: "отрицательный DialTimeout", mutate: func(c *mail.Config) { c.DialTimeout = -time.Second }, want: mail.ErrConfig},
		{name: "отрицательный SendTimeout", mutate: func(c *mail.Config) { c.SendTimeout = -time.Second }, want: mail.ErrConfig},
		{name: "отрицательный SendRate", mutate: func(c *mail.Config) { c.SendRate = -1 }, want: mail.ErrConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tc.mutate(&cfg)

			if _, err := mail.NewSMTP(cfg); !errors.Is(err, tc.want) {
				t.Fatalf("ошибка %v, ожидалась %v", err, tc.want)
			}
		})
	}
}

func TestNewSMTPAcceptsConfig(t *testing.T) {
	t.Parallel()

	for _, cfg := range []mail.Config{
		validConfig(),
		func() mail.Config { c := validConfig(); c.Port = 1; c.TLS = mail.TLSImplicit; return c }(),
		func() mail.Config { c := validConfig(); c.Port = 65535; c.From = "noreply@dpp.example"; return c }(),
	} {
		if _, err := mail.NewSMTP(cfg); err != nil {
			t.Fatalf("конфиг %+v: %v", cfg, err)
		}
	}
}

func TestConfigTimeoutWithoutSendTimeout(t *testing.T) {
	t.Parallel()

	cfg := mail.Config{SendRate: 1}
	if got := cfg.Timeout(1 << 20); got != 0 {
		t.Fatalf("срок %v при нулевом SendTimeout", got)
	}
}

func TestConfigTimeoutWithoutSendRate(t *testing.T) {
	t.Parallel()

	cfg := mail.Config{SendTimeout: 15 * time.Second}
	if got := cfg.Timeout(50 << 20); got != 15*time.Second {
		t.Fatalf("срок %v, ожидался неизменный SendTimeout", got)
	}
}

// TestConfigTimeoutMatchesDPP — фасад DPP заменит SendTimeout(size) на Config.Timeout без смены сроков.
func TestConfigTimeoutMatchesDPP(t *testing.T) {
	t.Parallel()

	const rate = 256 << 10

	dpp := func(size int) time.Duration { return 30*time.Second + time.Duration(size/rate)*time.Second }
	cfg := mail.Config{SendTimeout: 30 * time.Second, SendRate: rate}

	for _, size := range []int{0, 1, rate - 1, rate, rate + 1, 10 * rate, 25 << 20} {
		if got, want := cfg.Timeout(size), dpp(size); got != want {
			t.Fatalf("размер %d: срок %v, ожидался %v", size, got, want)
		}
	}
}

func startServer(t *testing.T, opts smtptest.Options) (*smtptest.Server, *mail.SMTP) {
	t.Helper()

	srv := smtptest.Start(t, opts)

	tlsMode := mail.TLSStartTLS
	if opts.Mode == smtptest.ImplicitTLS {
		tlsMode = mail.TLSImplicit
	}

	sender, err := mail.NewSMTP(mail.Config{
		Host:     smtptest.Host,
		Port:     srv.Port(),
		Username: "robot",
		Password: "secret",
		From:     "Паспорт объекта <noreply@dpp.example>",
		TLS:      tlsMode,
		RootCAs:  srv.RootCAs(),
	})
	if err != nil {
		t.Fatalf("NewSMTP: %v", err)
	}

	return srv, sender
}

func testLetter() mail.Letter {
	return mail.Letter{To: "Иван Петров <ivan@example.com>", Subject: "Проверка", Text: "Привет"}
}

func requireSendError(t *testing.T, err error, phase mail.Phase, outcome mail.Outcome) *mail.SendError {
	t.Helper()

	sendErr, ok := errors.AsType[*mail.SendError](err)
	if !ok {
		t.Fatalf("ошибка %v, ожидалась *SendError", err)
	}

	if sendErr.Phase != phase || sendErr.Outcome != outcome {
		t.Fatalf("фаза %s, исход %s; ожидались %s, %s", sendErr.Phase, sendErr.Outcome, phase, outcome)
	}

	return sendErr
}

func TestSMTPSendDelivers(t *testing.T) {
	t.Parallel()

	for name, mode := range map[string]smtptest.Mode{"implicit": smtptest.ImplicitTLS, "starttls": smtptest.StartTLS} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv, sender := startServer(t, smtptest.Options{Mode: mode})

			if err := sender.Send(t.Context(), testLetter()); err != nil {
				t.Fatalf("Send: %v", err)
			}

			session := srv.Wait(t)
			if session.From != "noreply@dpp.example" ||
				len(session.Rcpt) != 1 || session.Rcpt[0] != "ivan@example.com" {
				t.Fatalf("конверт MAIL %q, RCPT %q — ожидались голые адреса", session.From, session.Rcpt)
			}

			if !session.Encrypted || !session.Authenticated || session.Username != "robot" {
				t.Fatalf("сессия: TLS %v, AUTH %v, логин %q",
					session.Encrypted, session.Authenticated, session.Username)
			}

			msg, err := netmail.ReadMessage(strings.NewReader(session.Data))
			if err != nil {
				t.Fatalf("тело не разбирается: %v", err)
			}

			to, err := msg.Header.AddressList("To")
			if err != nil || len(to) != 1 || to[0].Name != "Иван Петров" || to[0].Address != "ivan@example.com" {
				t.Fatalf("заголовок To %v: %v", to, err)
			}
		})
	}
}

func TestSMTPSendRequiresStartTLS(t *testing.T) {
	t.Parallel()

	srv, sender := startServer(t, smtptest.Options{Mode: smtptest.Plain})

	err := sender.Send(t.Context(), testLetter())
	if !errors.Is(err, mail.ErrNoTLS) {
		t.Fatalf("ошибка %v, ожидалась ErrNoTLS", err)
	}

	requireSendError(t, err, mail.PhaseTLS, mail.OutcomeRejected)

	if session := srv.Wait(t); session.Authenticated || session.From != "" {
		t.Fatalf("без TLS дошло до AUTH или MAIL: %+v", session)
	}
}

func TestSMTPSendRejected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		opts      smtptest.Options
		phase     mail.Phase
		code      int
		permanent bool
	}{
		{
			name:      "550 на RCPT",
			opts:      smtptest.Options{Mode: smtptest.StartTLS, RcptReply: "550 5.1.1 no such user"},
			phase:     mail.PhaseEnvelope,
			code:      550,
			permanent: true,
		},
		{
			name:  "451 на точку",
			opts:  smtptest.Options{Mode: smtptest.StartTLS, DataReply: "451 4.3.0 try later"},
			phase: mail.PhaseData,
			code:  451,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, sender := startServer(t, tc.opts)

			sendErr := requireSendError(t, sender.Send(t.Context(), testLetter()), tc.phase, mail.OutcomeRejected)
			if sendErr.Code != tc.code || sendErr.Permanent() != tc.permanent {
				t.Fatalf("код %d, Permanent %v; ожидались %d, %v",
					sendErr.Code, sendErr.Permanent(), tc.code, tc.permanent)
			}
		})
	}
}

func TestSMTPSendUnknownWithoutDataReply(t *testing.T) {
	t.Parallel()

	_, sender := startServer(t, smtptest.Options{Mode: smtptest.StartTLS, DropAfterData: true})

	sendErr := requireSendError(t, sender.Send(t.Context(), testLetter()), mail.PhaseData, mail.OutcomeUnknown)
	if sendErr.Code != 0 || sendErr.Permanent() {
		t.Fatalf("код %d без ответа сервера", sendErr.Code)
	}
}

// TestSMTPSendIgnoresQuitAfterData — принятое письмо не становится отказом из-за QUIT: иначе очередь шлёт его снова.
func TestSMTPSendIgnoresQuitAfterData(t *testing.T) {
	t.Parallel()

	for name, opts := range map[string]smtptest.Options{
		"ошибка":   {Mode: smtptest.StartTLS, QuitReply: "554 5.0.0 broken"},
		"обрыв":    {Mode: smtptest.StartTLS, DropOnQuit: true},
		"implicit": {Mode: smtptest.ImplicitTLS, DropOnQuit: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv, sender := startServer(t, opts)

			if err := sender.Send(t.Context(), testLetter()); err != nil {
				t.Fatalf("Send после принятого DATA: %v", err)
			}

			if session := srv.Wait(t); session.Data == "" {
				t.Fatal("сервер не получил тело")
			}
		})
	}
}

func TestSMTPSendCancelAtSilentServer(t *testing.T) {
	t.Parallel()

	_, sender := startServer(t, smtptest.Options{Mode: smtptest.StartTLS, Silent: true})

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	err := sender.Send(ctx, testLetter())

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("отмена вернула управление за %v", elapsed)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка %v, ожидалась context.Canceled", err)
	}

	requireSendError(t, err, mail.PhaseDial, mail.OutcomeRejected)
}

func TestSMTPSendTimeoutAtSilentServer(t *testing.T) {
	t.Parallel()

	srv := smtptest.Start(t, smtptest.Options{Mode: smtptest.ImplicitTLS, Silent: true})

	sender, err := mail.NewSMTP(mail.Config{
		Host:        smtptest.Host,
		Port:        srv.Port(),
		From:        "noreply@dpp.example",
		TLS:         mail.TLSImplicit,
		SendTimeout: 200 * time.Millisecond,
		RootCAs:     srv.RootCAs(),
	})
	if err != nil {
		t.Fatalf("NewSMTP: %v", err)
	}

	err = sender.Send(context.WithoutCancel(t.Context()), testLetter())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ошибка %v, ожидалась context.DeadlineExceeded", err)
	}

	requireSendError(t, err, mail.PhaseDial, mail.OutcomeRejected)
}

func TestSMTPSendRejectsInvalidRecipientOffline(t *testing.T) {
	t.Parallel()

	srv, sender := startServer(t, smtptest.Options{Mode: smtptest.StartTLS})

	letter := testLetter()
	letter.To = "ivan"

	err := sender.Send(t.Context(), letter)
	if !errors.Is(err, mail.ErrInvalidAddress) {
		t.Fatalf("ошибка %v, ожидалась ErrInvalidAddress", err)
	}

	if _, ok := errors.AsType[*mail.SendError](err); ok {
		t.Fatalf("ошибка Validate обёрнута в SendError: %v", err)
	}

	if n := srv.Connections(); n != 0 {
		t.Fatalf("сервер принял %d соединений", n)
	}
}
