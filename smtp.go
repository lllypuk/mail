package mail

import (
	"crypto/x509"
	"errors"
	"fmt"
	netmail "net/mail"
	"time"
)

var (
	// ErrInvalidFrom — адрес отправителя из конфигурации не разбирается.
	ErrInvalidFrom = errors.New("некорректный адрес отправителя")
	// ErrConfig — конфигурация SMTP не годится для соединения.
	ErrConfig = errors.New("некорректная конфигурация SMTP")
)

const (
	defaultDialTimeout = 10 * time.Second
	maxPort            = 65535
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
	from, err := netmail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrInvalidFrom, cfg.From, err)
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
