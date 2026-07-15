package portbind

import (
	"net"
	"testing"
)

func TestListenerPort_FromListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	want := ln.Addr().(*net.TCPAddr).Port
	got := ListenerPort(ln, 99999)
	if got != want {
		t.Errorf("ListenerPort(ln, 99999) = %d, want %d", got, want)
	}
}

func TestListenerPort_NilFallback(t *testing.T) {
	got := ListenerPort(nil, 49153)
	if got != 49153 {
		t.Errorf("ListenerPort(nil, 49153) = %d, want 49153", got)
	}
}
