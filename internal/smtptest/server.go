// Package smtptest — SMTP-сервер на 127.0.0.1 для тестов отправителя; сценарии отказа задаются Options.
package smtptest

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// Host — адрес сервера; на него выписан сертификат.
	Host = "127.0.0.1"

	waitTimeout = 5 * time.Second
	certTTL     = time.Hour
)

// Mode — как сервер включает TLS.
type Mode int

const (
	// Plain — без TLS и без расширения STARTTLS.
	Plain Mode = iota
	// StartTLS — STARTTLS обязателен до AUTH и MAIL.
	StartTLS
	// ImplicitTLS — TLS с первого байта.
	ImplicitTLS
)

// Options — режим и сценарий сервера; пустая строка ответа — успешный ответ.
type Options struct {
	Mode Mode
	// Silent — после приветствия команды читаются без ответа.
	Silent    bool
	RcptReply string
	// DataReply — ответ на точку после тела DATA.
	DataReply string
	// DropAfterData — закрыть соединение после точки без ответа.
	DropAfterData bool
	QuitReply     string
	DropOnQuit    bool
}

// Session — что сервер увидел за одно соединение.
type Session struct {
	From          string
	Rcpt          []string
	Data          string
	Username      string
	Authenticated bool
	Encrypted     bool
}

// Server принимает соединения до конца теста; каждое — отдельная Session.
type Server struct {
	opts     Options
	port     int
	roots    *x509.CertPool
	tls      *tls.Config
	ln       net.Listener
	done     chan Session
	quit     chan struct{}
	accepted atomic.Int64
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

// Start поднимает сервер; t.Cleanup закрывает его и рвёт незавершённые сессии.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()

	cert, roots := selfSignedCert(t)
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", Host+":0")
	if err != nil {
		t.Fatalf("smtptest: listen: %v", err)
	}

	if opts.Mode == ImplicitTLS {
		ln = tls.NewListener(ln, tlsConfig)
	}

	_, rawPort, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(rawPort)

	s := &Server{
		opts:  opts,
		port:  port,
		roots: roots,
		tls:   tlsConfig,
		ln:    ln,
		done:  make(chan Session),
		quit:  make(chan struct{}),
		conns: make(map[net.Conn]struct{}),
	}

	s.wg.Go(s.accept)
	t.Cleanup(s.close)

	return s
}

// Port — порт сервера.
func (s *Server) Port() int { return s.port }

// RootCAs — корень самоподписанного сертификата сервера.
func (s *Server) RootCAs() *x509.CertPool { return s.roots }

// Connections — сколько соединений сервер принял.
func (s *Server) Connections() int { return int(s.accepted.Load()) }

// Wait возвращает следующую закончившуюся сессию.
func (s *Server) Wait(t testing.TB) Session {
	t.Helper()

	select {
	case session := <-s.done:
		return session
	case <-time.After(waitTimeout):
		t.Fatal("smtptest: сессия не закончилась")
		return Session{}
	}
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}

		s.accepted.Add(1)
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()

		s.wg.Go(func() { s.serve(conn) })
	}
}

func (s *Server) close() {
	close(s.quit)
	_ = s.ln.Close()

	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
}

type conn struct {
	net.Conn

	r   *bufio.Reader
	rec Session
}

func (c *conn) reply(line string) {
	_, _ = io.WriteString(c, line+"\r\n")
}

func (c *conn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')

	return strings.TrimRight(line, "\r\n"), err
}

func (s *Server) serve(raw net.Conn) {
	c := &conn{Conn: raw, r: bufio.NewReader(raw), rec: Session{Encrypted: s.opts.Mode == ImplicitTLS}}

	defer func() {
		_ = c.Close()

		s.mu.Lock()
		delete(s.conns, raw)
		s.mu.Unlock()

		select {
		case s.done <- c.rec:
		case <-s.quit:
		}
	}()

	c.reply("220 smtptest ESMTP")

	for {
		line, err := c.readLine()
		if err != nil {
			return
		}

		if s.opts.Silent {
			continue
		}

		verb, arg, _ := strings.Cut(line, " ")
		if !s.handle(c, strings.ToUpper(verb), arg) {
			return
		}
	}
}

// handle отвечает на команду; false — сессия кончена.
func (s *Server) handle(c *conn, verb, arg string) bool {
	needTLS := s.opts.Mode == StartTLS && !c.rec.Encrypted

	switch {
	case verb == "EHLO" || verb == "HELO":
		if needTLS {
			c.reply("250-smtptest\r\n250 STARTTLS")
		} else {
			c.reply("250-smtptest\r\n250 AUTH PLAIN")
		}
	case verb == "STARTTLS":
		return s.startTLS(c, needTLS)
	case needTLS && (verb == "AUTH" || verb == "MAIL" || verb == "RCPT" || verb == "DATA"):
		c.reply("530 5.7.0 STARTTLS first")
	case verb == "AUTH":
		return auth(c, arg)
	case verb == "MAIL":
		c.rec.From = path(arg, "FROM:")
		c.reply("250 2.1.0 ok")
	case verb == "RCPT":
		c.rec.Rcpt = append(c.rec.Rcpt, path(arg, "TO:"))
		c.reply(orDefault(s.opts.RcptReply, "250 2.1.5 ok"))
	case verb == "DATA":
		return s.data(c)
	case verb == "QUIT":
		if !s.opts.DropOnQuit {
			c.reply(orDefault(s.opts.QuitReply, "221 2.0.0 bye"))
		}

		return false
	case verb == "RSET" || verb == "NOOP":
		c.reply("250 2.0.0 ok")
	default:
		c.reply("502 5.5.1 not implemented")
	}

	return true
}

func (s *Server) startTLS(c *conn, allowed bool) bool {
	if !allowed {
		c.reply("502 5.5.1 STARTTLS not available")
		return true
	}

	c.reply("220 2.0.0 go ahead")

	secure := tls.Server(c.Conn, s.tls)
	if err := secure.HandshakeContext(context.Background()); err != nil {
		return false
	}

	c.Conn, c.r, c.rec.Encrypted = secure, bufio.NewReader(secure), true

	return true
}

func auth(c *conn, arg string) bool {
	mech, resp, _ := strings.Cut(arg, " ")
	if !strings.EqualFold(mech, "PLAIN") {
		c.reply("504 5.5.4 mechanism not supported")
		return true
	}

	if resp == "" {
		c.reply("334 ")

		var err error
		if resp, err = c.readLine(); err != nil {
			return false
		}
	}

	raw, err := base64.StdEncoding.DecodeString(resp)
	parts := strings.Split(string(raw), "\x00")
	if err != nil || len(parts) != 3 {
		c.reply("501 5.5.2 bad PLAIN response")
		return true
	}

	c.rec.Username, c.rec.Authenticated = parts[1], true
	c.reply("235 2.7.0 ok")

	return true
}

func (s *Server) data(c *conn) bool {
	c.reply("354 go ahead")

	var body strings.Builder
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.rec.Data = body.String()
			return false
		}

		if line == ".\r\n" {
			break
		}

		body.WriteString(strings.TrimPrefix(line, "."))
	}

	c.rec.Data = body.String()

	if s.opts.DropAfterData {
		return false
	}

	c.reply(orDefault(s.opts.DataReply, "250 2.0.0 accepted"))

	return true
}

// path достаёт адрес из «FROM:<a@b> PARAM=…».
func path(arg, prefix string) string {
	if len(arg) >= len(prefix) && strings.EqualFold(arg[:len(prefix)], prefix) {
		arg = arg[len(prefix):]
	}

	addr, _, _ := strings.Cut(strings.TrimSpace(arg), " ")

	return strings.Trim(addr, "<>")
}

func orDefault(reply, fallback string) string {
	if reply == "" {
		return fallback
	}

	return reply
}

func selfSignedCert(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("smtptest: ключ: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "smtptest"},
		NotBefore:             time.Now().Add(-certTTL),
		NotAfter:              time.Now().Add(certTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP(Host)},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("smtptest: сертификат: %v", err)
	}

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("smtptest: разбор сертификата: %v", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(parsed)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}
