package network

import (
	"errors"
	"net"
	"strconv"
	"testing"
)

func TestCheckTCPPortAvailable_portInUse(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	err = CheckTCPPortAvailable(port, "test listener")
	if err == nil {
		t.Fatal("expected port-in-use error")
	}
	var portErr *PortInUseError
	if !errors.As(err, &portErr) {
		t.Fatalf("expected *PortInUseError, got %T: %v", err, err)
	}
	if portErr.Port != port {
		t.Fatalf("Port = %q, want %q", portErr.Port, port)
	}
}

func TestCheckTCPPortAvailable_freePort(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if err := ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}

	if err := CheckTCPPortAvailable(port, "test listener"); err != nil {
		t.Fatalf("CheckTCPPortAvailable: %v", err)
	}
}
