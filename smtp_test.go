package mail_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lllypuk/mail"
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
