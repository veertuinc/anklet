package network

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

// PortInUseError is returned when a TCP port is already bound by another process.
type PortInUseError struct {
	Port    string
	Purpose string
}

func (e *PortInUseError) Error() string {
	if e.Purpose != "" {
		return fmt.Sprintf(
			"port %s is already in use (%s); another anklet instance may already be running — stop it or choose a different port in your config",
			e.Port,
			e.Purpose,
		)
	}
	return fmt.Sprintf(
		"port %s is already in use; another anklet instance may already be running — stop it or choose a different port in your config",
		e.Port,
	)
}

// CheckTCPPortAvailable verifies that port can be bound for listening.
func CheckTCPPortAvailable(port, purpose string) error {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return &PortInUseError{Port: port, Purpose: purpose}
		}
		return fmt.Errorf("unable to bind port %s (%s): %w", port, purpose, err)
	}
	return ln.Close()
}
