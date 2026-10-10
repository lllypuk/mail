package mail

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"os"
	"regexp"
)

// ErrNoTLS — сервер не предлагает STARTTLS; по открытому каналу ушли бы логин и пароль ящика.
var ErrNoTLS = errors.New("сервер не поддерживает TLS")

// permanentCode — с него начинаются окончательные отказы SMTP (RFC 5321 §4.2.1).
const permanentCode = 500

// Phase — шаг сессии SMTP, на котором Send получил отказ.
type Phase string

const (
	// PhaseDial — соединение, приветствие сервера и EHLO.
	PhaseDial Phase = "dial"
	// PhaseTLS — рукопожатие implicit TLS или STARTTLS.
	PhaseTLS Phase = "tls"
	// PhaseAuth — AUTH.
	PhaseAuth Phase = "auth"
	// PhaseEnvelope — MAIL FROM и RCPT TO.
	PhaseEnvelope Phase = "envelope"
	// PhaseData — DATA, тело и ответ на точку.
	PhaseData Phase = "data"
)

// Outcome — что известно о письме после отказа.
type Outcome string

const (
	// OutcomeRejected — письма у сервера нет: повтор не задвоит.
	OutcomeRejected Outcome = "rejected"
	// OutcomeUnknown — тело ушло, финального ответа нет: письмо могло дойти, повтор может задвоить.
	OutcomeUnknown Outcome = "unknown"
)

// SendError — отказ Send после начала соединения.
type SendError struct {
	Phase   Phase
	Outcome Outcome
	// Code — код ответа SMTP; 0 — ответа не было.
	Code int
	Err  error
}

// Error прячет адреса во всём тексте причины: сервер эхом возвращает адрес и в ответах, и в битых строках.
func (e *SendError) Error() string {
	cause := "<nil>"
	if e.Err != nil {
		cause = addressPattern.ReplaceAllString(e.Err.Error(), "***")
	}

	return fmt.Sprintf("отправка письма (%s, %s): %s", e.Phase, e.Outcome, cause)
}

func (e *SendError) Unwrap() error { return e.Err }

// Permanent — сервер отказал окончательно (5xx), повтор того же письма не поможет.
func (e *SendError) Permanent() bool { return e.Code >= permanentCode }

// sendError собирает отказ; отмена ctx и дедлайн соединения видны через errors.Is как ошибки context.
func sendError(ctx context.Context, phase Phase, outcome Outcome, err error) *SendError {
	switch cause := ctx.Err(); {
	case cause != nil && !errors.Is(err, cause):
		err = fmt.Errorf("%w: %w", cause, err)
	case cause == nil && errors.Is(err, os.ErrDeadlineExceeded):
		err = fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
	}

	var code int

	if protoErr, ok := errors.AsType[*textproto.Error](err); ok {
		code = protoErr.Code
	}

	return &SendError{Phase: phase, Outcome: outcome, Code: code, Err: err}
}

// addressPattern — слово с @: Postfix и другие эхом возвращают адрес из MAIL и RCPT.
var addressPattern = regexp.MustCompile(`[^\s<>]+@[^\s<>]+`)
