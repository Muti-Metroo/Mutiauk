package socks5

import (
	"fmt"
	"io"
	"net"
)

// Authenticator handles SOCKS5 authentication
type Authenticator interface {
	// Method returns the authentication method byte
	Method() byte

	// Authenticate performs authentication on the connection
	Authenticate(conn net.Conn) error
}

// NoAuth implements no authentication (method 0x00)
type NoAuth struct{}

// Method returns 0x00 (no authentication)
func (a *NoAuth) Method() byte {
	return AuthNone
}

// Authenticate does nothing for no-auth
func (a *NoAuth) Authenticate(conn net.Conn) error {
	return nil
}

// UserPassAuth implements username/password authentication (method 0x02)
type UserPassAuth struct {
	Username string
	Password string
}

// Method returns 0x02 (username/password)
func (a *UserPassAuth) Method() byte {
	return AuthPassword
}

// Authenticate performs username/password authentication
// RFC 1929 format:
//
//	+----+------+----------+------+----------+
//	|VER | ULEN |  UNAME   | PLEN |  PASSWD  |
//	+----+------+----------+------+----------+
//	| 1  |  1   | 1 to 255 |  1   | 1 to 255 |
//	+----+------+----------+------+----------+
func (a *UserPassAuth) Authenticate(conn net.Conn) error {
	ulen := len(a.Username)
	plen := len(a.Password)

	if ulen > 255 || plen > 255 {
		return fmt.Errorf("username or password too long")
	}

	// Build auth request
	// VER (0x01 for username/password auth) + ULEN + USERNAME + PLEN + PASSWORD
	req := make([]byte, 1+1+ulen+1+plen)
	req[0] = 0x01 // Version of username/password auth
	req[1] = byte(ulen)
	copy(req[2:], a.Username)
	req[2+ulen] = byte(plen)
	copy(req[3+ulen:], a.Password)

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("failed to send auth request: %w", err)
	}

	// Read response
	// +----+--------+
	// |VER | STATUS |
	// +----+--------+
	// | 1  |   1    |
	// +----+--------+
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	if resp[1] != 0x00 {
		return fmt.Errorf("authentication failed: status %d", resp[1])
	}

	return nil
}

// NewAuthenticator creates an authenticator based on credentials
func NewAuthenticator(username, password string) Authenticator {
	if username == "" && password == "" {
		return &NoAuth{}
	}
	return &UserPassAuth{
		Username: username,
		Password: password,
	}
}
