package server

import (
	"fmt"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/gortc/gortcd/internal/allocator"
	"github.com/gortc/gortcd/internal/auth"
	"github.com/gortc/gortcd/internal/testutil"
	"github.com/gortc/stun"
)

func isErr(m *stun.Message) bool {
	return m.Type.Class == stun.ClassErrorResponse
}

func do(logger *zap.Logger, req, res *stun.Message, c *net.UDPConn, attrs ...stun.Setter) error {
	start := time.Now()
	if err := req.Build(attrs...); err != nil {
		logger.Error("failed to build", zap.Error(err))
		return err
	}
	if _, err := req.WriteTo(c); err != nil {
		logger.Error("failed to write",
			zap.Error(err), zap.Stringer("m", req),
		)
		return err
	}
	logger.Info("sent message", zap.Stringer("m", req), zap.Stringer("t", req.Type))
	if cap(res.Raw) < 800 {
		res.Raw = make([]byte, 0, 1024)
	}
	res.Reset()
	c.SetReadDeadline(time.Now().Add(time.Second * 2))
	_, err := res.ReadFrom(c)
	if err != nil {
		logger.Error("failed to read",
			zap.Error(err), zap.Stringer("m", req),
		)
		return err
	}
	if req.Type.Class != stun.ClassIndication && req.TransactionID != res.TransactionID {
		return fmt.Errorf("transaction ID mismatch: %x (got) != %x (expected)",
			req.TransactionID, res.TransactionID,
		)
	}
	logger.Info("got message",
		zap.Stringer("m", res),
		zap.Stringer("t", res.Type),
		zap.Duration("rtt", time.Since(start)),
	)
	return nil
}

func listenUDP(t testing.TB, addrs ...string) (*net.UDPConn, *net.UDPAddr) {
	addr := "127.0.0.1:0"
	if len(addrs) > 0 {
		addr = addrs[0]
	}
	rAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", rAddr)
	if err != nil {
		t.Fatal(err)
	}
	udpAddr, err := net.ResolveUDPAddr("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	return conn, udpAddr
}

func TestServer_processBindingRequest(t *testing.T) {
	serverConn, _ := listenUDP(t)
	defer serverConn.Close()
	s, err := New(Options{
		Log:  zap.NewNop(),
		Conn: serverConn,
		Auth: auth.NewStatic([]auth.StaticCredential{
			{Username: "username", Password: "secret", Realm: "realm"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 34567}
	)
	m := stun.MustBuild(stun.BindingRequest, stun.Fingerprint)
	ctx := &context{
		request:  new(stun.Message),
		response: new(stun.Message),
	}
	ctx.request.Raw = make([]byte, len(m.Raw))
	ctx.request.Raw = ctx.request.Raw[:len(m.Raw)]
	ctx.client = allocator.Addr{
		IP:   addr.IP,
		Port: addr.Port,
	}
	copy(ctx.request.Raw, m.Raw)
	if err := s.process(ctx); err != nil {
		t.Fatal(err)
	}
	t.Run("ZeroAlloc", func(t *testing.T) {
		ctx.request.Raw = ctx.request.Raw[:len(m.Raw)]
		ctx.client = allocator.Addr{
			IP:   addr.IP,
			Port: addr.Port,
		}
		copy(ctx.request.Raw, m.Raw)
		testutil.ShouldNotAllocate(t, func() {
			s.process(ctx)
		})
	})
}

func BenchmarkServer_processBindingRequest(b *testing.B) {
	b.ReportAllocs()
	serverConn, _ := listenUDP(b)
	defer serverConn.Close()
	s, err := New(Options{
		Log:  zap.NewNop(),
		Conn: serverConn,
		Auth: auth.NewStatic([]auth.StaticCredential{
			{Username: "username", Password: "secret", Realm: "realm"},
		}),
	})
	if err != nil {
		b.Fatal(err)
	}
	var (
		addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 34567}
	)
	m := stun.MustBuild(stun.BindingRequest, stun.Fingerprint)
	b.ResetTimer()
	ctx := &context{
		request:  new(stun.Message),
		response: new(stun.Message),
	}
	ctx.request.Raw = make([]byte, len(m.Raw))
	for i := 0; i < b.N; i++ {
		ctx.request.Raw = ctx.request.Raw[:len(m.Raw)]
		ctx.client = allocator.Addr{
			IP:   addr.IP,
			Port: addr.Port,
		}
		copy(ctx.request.Raw, m.Raw)
		if err := s.process(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func TestServer_notStun(t *testing.T) {
	serverConn, _ := listenUDP(t)
	defer serverConn.Close()
	s, err := New(Options{
		Log:  zap.NewNop(),
		Conn: serverConn,
		Auth: auth.NewStatic([]auth.StaticCredential{
			{Username: "username", Password: "secret", Realm: "realm"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 34567}
	)
	buf := make([]byte, 512)
	for i := range buf {
		buf[i] = byte(i % 127)
	}
	ctx := &context{
		request:  new(stun.Message),
		response: new(stun.Message),
	}
	ctx.request.Raw = make([]byte, len(buf), 1024)
	copy(ctx.request.Raw, buf)
	ctx.client = allocator.Addr{
		IP:   addr.IP,
		Port: addr.Port,
	}
	if err := s.process(ctx); err != errNotSTUNMessage {
		t.Fatal(err)
	}
	t.Run("ZeroAlloc", func(t *testing.T) {
		ctx.request.Raw = ctx.request.Raw[:len(buf)]
		copy(ctx.request.Raw, buf)
		ctx.client = allocator.Addr{
			IP:   addr.IP,
			Port: addr.Port,
		}
		testutil.ShouldNotAllocate(t, func() {
			s.process(ctx)
		})
	})
}

func TestServer_badRequest(t *testing.T) {
	serverConn, _ := listenUDP(t)
	defer serverConn.Close()
	s, err := New(Options{
		Log:  zap.NewNop(),
		Conn: serverConn,
		Auth: auth.NewStatic([]auth.StaticCredential{
			{Username: "username", Password: "secret", Realm: "realm"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 34567}
	)
	m := stun.MustBuild(stun.BindingRequest, stun.Fingerprint)
	m.Raw = m.Raw[:len(m.Raw)-4]
	ctx := &context{
		request:  new(stun.Message),
		response: new(stun.Message),
	}
	ctx.request.Raw = make([]byte, len(m.Raw))
	ctx.request.Raw = ctx.request.Raw[:len(m.Raw)]
	ctx.client = allocator.Addr{
		IP:   addr.IP,
		Port: addr.Port,
	}
	copy(ctx.request.Raw, m.Raw)
	if err := s.process(ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.response.Length != 0 {
		t.Error("unexpected response")
	}
}

func TestServer_badFingerprint(t *testing.T) {
	serverConn, _ := listenUDP(t)
	defer serverConn.Close()
	s, err := New(Options{
		Log:  zap.NewNop(),
		Conn: serverConn,
		Auth: auth.NewStatic([]auth.StaticCredential{
			{Username: "username", Password: "secret", Realm: "realm"},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 34567}
	)
	m := stun.MustBuild(stun.BindingRequest)
	m.Add(stun.AttrFingerprint, []byte{1, 2, 3, 4})
	ctx := &context{
		request:  new(stun.Message),
		response: new(stun.Message),
	}
	ctx.request.Raw = make([]byte, len(m.Raw))
	ctx.request.Raw = ctx.request.Raw[:len(m.Raw)]
	ctx.client = allocator.Addr{
		IP:   addr.IP,
		Port: addr.Port,
	}
	copy(ctx.request.Raw, m.Raw)
	if err := s.process(ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.response.Type.Class != stun.ClassErrorResponse {
		t.Error("unexpected success")
	}
}
