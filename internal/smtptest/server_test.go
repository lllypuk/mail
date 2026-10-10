package smtptest_test

import (
	"crypto/tls"
	"io"
	"net"
	"net/smtp"
	"slices"
	"strconv"
	"testing"

	"github.com/lllypuk/mail/internal/smtptest"
)

func TestServerRecordsSTARTTLSSession(t *testing.T) {
	t.Parallel()

	srv := smtptest.Start(t, smtptest.Options{Mode: smtptest.StartTLS})

	client, err := smtp.Dial(net.JoinHostPort(smtptest.Host, strconv.Itoa(srv.Port())))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	if ok, _ := client.Extension("AUTH"); ok {
		t.Error("AUTH объявлен до STARTTLS")
	}

	if err = client.Mail("early@example.com"); err == nil {
		t.Error("MAIL до STARTTLS принят")
	}

	deliver(t, client, srv)

	got := srv.Wait(t)
	if !got.Encrypted || !got.Authenticated || got.Username != "user@example.com" {
		t.Errorf("TLS/AUTH: %+v", got)
	}

	if got.From != "from@example.com" || !slices.Equal(got.Rcpt, []string{"to@example.com"}) {
		t.Errorf("конверт: from %q, rcpt %q", got.From, got.Rcpt)
	}

	if want := "Subject: hi\r\n\r\n.dot\r\nbody\r\n"; got.Data != want {
		t.Errorf("тело %q, ждали %q", got.Data, want)
	}

	if srv.Connections() != 1 {
		t.Errorf("соединений %d, ждали 1", srv.Connections())
	}
}

// deliver проводит клиента через STARTTLS, AUTH и DATA.
func deliver(t *testing.T, client *smtp.Client, srv *smtptest.Server) {
	t.Helper()

	steps := []func() error{
		func() error {
			return client.StartTLS(&tls.Config{
				ServerName: smtptest.Host,
				RootCAs:    srv.RootCAs(),
				MinVersion: tls.VersionTLS12,
			})
		},
		func() error { return client.Auth(smtp.PlainAuth("", "user@example.com", "secret", smtptest.Host)) },
		func() error { return client.Mail("from@example.com") },
		func() error { return client.Rcpt("to@example.com") },
		func() error {
			w, dataErr := client.Data()
			if dataErr != nil {
				return dataErr
			}

			if _, dataErr = io.WriteString(w, "Subject: hi\r\n\r\n.dot\r\nbody\r\n"); dataErr != nil {
				return dataErr
			}

			return w.Close()
		},
		client.Quit,
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("шаг %d: %v", i, err)
		}
	}
}
