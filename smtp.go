package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"
)

var (
	// ErrInvalidFrom — адрес отправителя из конфигурации не разбирается или с local-part в кавычках.
	ErrInvalidFrom = errors.New("некорректный адрес отправителя")
	// ErrConfig — конфигурация SMTP не годится для соединения.
	ErrConfig = errors.New("некорректная конфигурация SMTP")
)

const (
	defaultDialTimeout = 10 * time.Second
	maxPort            = 65535
	// helloName — имя в EHLO, как у net/smtp по умолчанию.
	helloName = "localhost"
)

// TLSMode — как сессия переходит в TLS; номер порта библиотека не толкует.
type TLSMode int

const (
	// TLSStartTLS — открытое соединение и STARTTLS до AUTH; без расширения письмо не уходит.
	TLSStartTLS TLSMode = iota
	// TLSImplicit — TLS до приветствия сервера (SMTPS).
	TLSImplicit
)

// Config — параметры SMTP-сервера и сроки сессии.
type Config struct {
	Host string
	Port int
	// Username пустой — AUTH не выполняется.
	Username string
	Password string
	// From — отправитель RFC 5322: голый адрес уходит в MAIL FROM, с именем — в заголовок.
	From string
	TLS  TLSMode
	// DialTimeout 0 — 10 с.
	DialTimeout time.Duration
	// SendTimeout 0 — своего срока у сессии нет, только ctx.
	SendTimeout time.Duration
	// SendRate — байт письма в секунду, на которые растёт SendTimeout; 0 — без роста.
	SendRate int
	// RootCAs nil — системные корни.
	RootCAs *x509.CertPool
}

// Timeout — срок сессии для сообщения size байт; 0 — срок не ставится.
func (c Config) Timeout(size int) time.Duration {
	if c.SendTimeout == 0 {
		return 0
	}

	if c.SendRate == 0 {
		return c.SendTimeout
	}

	return c.SendTimeout + time.Duration(size/c.SendRate)*time.Second
}

// SMTP отправляет письма через SMTP-сервер с аутентификацией поверх TLS.
type SMTP struct {
	cfg  Config
	from netmail.Address
}

// NewSMTP проверяет конфигурацию сразу: кривой From роняет старт, а не первое письмо.
func NewSMTP(cfg Config) (*SMTP, error) {
	from, err := parseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFrom, err)
	}

	switch {
	case cfg.Host == "":
		return nil, fmt.Errorf("%w: пустой Host", ErrConfig)
	case cfg.Port < 1 || cfg.Port > maxPort:
		return nil, fmt.Errorf("%w: Port %d вне 1..%d", ErrConfig, cfg.Port, maxPort)
	case cfg.TLS != TLSStartTLS && cfg.TLS != TLSImplicit:
		return nil, fmt.Errorf("%w: неизвестный TLSMode %d", ErrConfig, cfg.TLS)
	case cfg.DialTimeout < 0, cfg.SendTimeout < 0:
		return nil, fmt.Errorf("%w: отрицательный срок", ErrConfig)
	case cfg.SendRate < 0:
		return nil, fmt.Errorf("%w: отрицательный SendRate", ErrConfig)
	}

	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = defaultDialTimeout
	}

	return &SMTP{cfg: cfg, from: *from}, nil
}

// Send отправляет письмо; nil — сервер принял DATA, иначе *SendError или ошибка Validate.
// Отмена ctx рвёт соединение; ошибка QUIT после принятого DATA не возвращается.
func (s *SMTP) Send(ctx context.Context, letter Letter) error {
	if err := letter.Validate(); err != nil {
		return err
	}

	rcpt, _ := letter.recipient()
	msg := letter.Encode(s.from.String())

	if timeout := s.cfg.Timeout(len(msg)); timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	dialer := &net.Dialer{Timeout: s.cfg.DialTimeout}

	raw, err := dialer.DialContext(ctx, "tcp", s.addr())
	if err != nil {
		return sendError(ctx, PhaseDial, OutcomeRejected, fmt.Errorf("подключиться к %s: %w", s.addr(), err))
	}

	defer func() { _ = raw.Close() }()

	// net/smtp не знает контекста: без дедлайна и закрытия по отмене замолчавший сервер держал бы воркер.
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()

	if deadline, ok := ctx.Deadline(); ok {
		if err = raw.SetDeadline(deadline); err != nil {
			return sendError(ctx, PhaseDial, OutcomeRejected, fmt.Errorf("выставить дедлайн соединения: %w", err))
		}
	}

	conn := raw
	if s.cfg.TLS == TLSImplicit {
		secure := tls.Client(raw, s.tlsConfig())
		if err = secure.HandshakeContext(ctx); err != nil {
			return sendError(ctx, PhaseTLS, OutcomeRejected, fmt.Errorf("рукопожатие TLS с %s: %w", s.addr(), err))
		}

		conn = secure
	}

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return sendError(ctx, PhaseDial, OutcomeRejected, fmt.Errorf("поздороваться с %s: %w", s.cfg.Host, err))
	}

	defer func() { _ = client.Close() }()

	// Явный EHLO: ленивый внутри Extension глотает отказ, и молчащий сервер выглядел бы как «нет STARTTLS».
	if err = client.Hello(helloName); err != nil {
		return sendError(ctx, PhaseDial, OutcomeRejected, fmt.Errorf("EHLO на %s: %w", s.cfg.Host, err))
	}

	if err = s.startTLS(client); err != nil {
		return sendError(ctx, PhaseTLS, OutcomeRejected, err)
	}

	if err = s.auth(client); err != nil {
		return sendError(ctx, PhaseAuth, OutcomeRejected, err)
	}

	if err = s.envelope(client, rcpt.Address); err != nil {
		return sendError(ctx, PhaseEnvelope, OutcomeRejected, err)
	}

	if err = data(ctx, client, msg); err != nil {
		return err
	}

	_ = client.Quit()

	return nil
}

func (s *SMTP) addr() string {
	return net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
}

func (s *SMTP) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.cfg.RootCAs}
}

func (s *SMTP) startTLS(client *smtp.Client) error {
	if s.cfg.TLS == TLSImplicit {
		return nil
	}

	if ok, _ := client.Extension("STARTTLS"); !ok {
		return fmt.Errorf("%w: %s", ErrNoTLS, s.addr())
	}

	if err := client.StartTLS(s.tlsConfig()); err != nil {
		return fmt.Errorf("перейти на TLS с %s: %w", s.cfg.Host, err)
	}

	return nil
}

// auth пропускается при пустом логине: это релей без пароля, а не забытая настройка.
func (s *SMTP) auth(client *smtp.Client) error {
	if s.cfg.Username == "" {
		return nil
	}

	if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
		return fmt.Errorf("аутентифицироваться на %s: %w", s.cfg.Host, err)
	}

	return nil
}

func (s *SMTP) envelope(client *smtp.Client, rcpt string) error {
	if err := client.Mail(s.from.Address); err != nil {
		return fmt.Errorf("объявить отправителя: %w", err)
	}

	if err := client.Rcpt(rcpt); err != nil {
		return fmt.Errorf("объявить получателя: %w", err)
	}

	return nil
}

// data передаёт тело; без ответа сервера на точку исход неизвестен — письмо могло дойти.
func data(ctx context.Context, client *smtp.Client, msg []byte) error {
	w, err := client.Data()
	if err != nil {
		return sendError(ctx, PhaseData, OutcomeRejected, fmt.Errorf("начать передачу письма: %w", err))
	}

	if _, err = w.Write(msg); err != nil {
		return sendError(ctx, PhaseData, OutcomeRejected, fmt.Errorf("передать письмо: %w", err))
	}

	if err = w.Close(); err != nil {
		outcome := OutcomeUnknown
		if _, replied := errors.AsType[*textproto.Error](err); replied {
			outcome = OutcomeRejected
		}

		return sendError(ctx, PhaseData, outcome, fmt.Errorf("завершить передачу письма: %w", err))
	}

	return nil
}
