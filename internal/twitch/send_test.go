package twitch

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
)

// fakeIRC is a local TLS server that stands in for Twitch: it completes enough
// of the handshake to keep the client alive and records the lines it receives.
type fakeIRC struct {
	ln    net.Listener
	addr  string
	lines chan string
}

func newFakeIRC(t *testing.T) *fakeIRC {
	t.Helper()
	cert := selfSignedCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIRC{ln: ln, addr: ln.Addr().String(), lines: make(chan string, 64)}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			select {
			case f.lines <- line:
			default:
			}
			// Emit ROOMSTATE once the client joins, mirroring real Twitch.
			if strings.HasPrefix(line, "JOIN") {
				conn.Write([]byte("@room-id=123 :tmi.twitch.tv ROOMSTATE #buh\r\n"))
			}
		}
	}()
	return f
}

func (f *fakeIRC) close() { f.ln.Close() }

// waitFor returns the first received line matching pred, or fails on timeout.
func (f *fakeIRC) waitFor(t *testing.T, what string, pred func(string) bool) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-f.lines:
			if pred(line) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func hasPrefix(p string) func(string) bool {
	return func(s string) bool { return strings.HasPrefix(s, p) }
}

func TestSendWritesPrivmsg(t *testing.T) {
	f := newFakeIRC(t)
	defer f.close()

	c := &Client{Channel: "buh", Nick: "nyx", Token: "tok", Out: make(chan chat.Message, 8), addr: f.addr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// The authenticated handshake must go out before a send is meaningful.
	if got := f.waitFor(t, "PASS", hasPrefix("PASS ")); got != "PASS oauth:tok" {
		t.Errorf("auth line = %q, want PASS oauth:tok", got)
	}
	f.waitFor(t, "JOIN", hasPrefix("JOIN "))

	c.Send("hello world")
	if got := f.waitFor(t, "PRIVMSG", hasPrefix("PRIVMSG")); got != "PRIVMSG #buh :hello world" {
		t.Errorf("got %q, want PRIVMSG #buh :hello world", got)
	}
}

// An anonymous client connects with a justinfan nick and never authenticates,
// so it must not attempt to send.
func TestAnonymousClientHandshakeAndNoSend(t *testing.T) {
	f := newFakeIRC(t)
	defer f.close()

	c := &Client{Channel: "buh", Out: make(chan chat.Message, 8), addr: f.addr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if got := f.waitFor(t, "NICK", hasPrefix("NICK ")); !strings.HasPrefix(got, "NICK justinfan") {
		t.Errorf("anonymous nick = %q, want a justinfan name", got)
	}

	// Send on an anonymous client is a no-op; nothing should reach the server.
	c.Send("should be dropped")
	select {
	case line := <-f.lines:
		if strings.HasPrefix(line, "PRIVMSG") {
			t.Errorf("anonymous client sent %q", line)
		}
	case <-time.After(300 * time.Millisecond):
		// good: no PRIVMSG
	}
}

func TestSendIgnoresBlank(t *testing.T) {
	c := &Client{Channel: "buh", Nick: "n", Token: "t"}
	c.Send("")
	c.Send("   ")
	select {
	case <-c.sendChan():
		t.Error("a blank message was queued")
	default:
	}
}

// selfSignedCert makes a throwaway cert so the fake server can speak TLS.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
