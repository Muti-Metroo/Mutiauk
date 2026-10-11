package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Client is a Unix socket API client
type Client struct {
	socketPath string
	timeout    time.Duration
}

// NewClient creates a new API client
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		timeout:    5 * time.Second,
	}
}

// SetTimeout sets the connection timeout
func (c *Client) SetTimeout(d time.Duration) {
	c.timeout = d
}

// IsRunning checks if the daemon is running by attempting to connect
func (c *Client) IsRunning() bool {
	conn, err := dialEndpoint(c.socketPath, c.timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Call makes an API call to the daemon
func (c *Client) Call(method string, params any) (*Response, error) {
	conn, err := dialEndpoint(c.socketPath, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer conn.Close()

	// Set read/write deadline
	conn.SetDeadline(time.Now().Add(c.timeout))

	// Build request
	req := Request{Method: method, ID: 1}
	if params != nil {
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal params: %w", err)
		}
		req.Params = paramsJSON
	}

	// Send request
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Read response
	var resp Response
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return &resp, nil
}

// Status gets the daemon status
func (c *Client) Status() (*StatusResult, error) {
	resp, err := c.Call("status", nil)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}

	var result StatusResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal status: %w", err)
	}

	return &result, nil
}

// RouteList gets the list of active routes
func (c *Client) RouteList() (*RouteListResult, error) {
	resp, err := c.Call("route.list", nil)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}

	var result RouteListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal routes: %w", err)
	}

	return &result, nil
}

// RouteAdd adds a route
func (c *Client) RouteAdd(destination, comment string, persist bool) (*RouteAddResult, error) {
	params := RouteAddParams{
		Destination: destination,
		Comment:     comment,
		Persist:     persist,
	}

	resp, err := c.Call("route.add", params)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}

	var result RouteAddResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

// RouteRemove removes a route
func (c *Client) RouteRemove(destination string, persist bool) (*RouteRemoveResult, error) {
	params := RouteRemoveParams{
		Destination: destination,
		Persist:     persist,
	}

	resp, err := c.Call("route.remove", params)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}

	var result RouteRemoveResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

// RouteTrace traces routing for a destination
func (c *Client) RouteTrace(destination string) (*RouteTraceResult, error) {
	params := RouteTraceParams{
		Destination: destination,
	}

	resp, err := c.Call("route.trace", params)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}

	var result RouteTraceResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

// HostList gets the current mesh host map from the daemon.
func (c *Client) HostList() (*HostListResult, error) {
	resp, err := c.Call("host.list", nil)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}

	var result HostListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal hosts: %w", err)
	}

	return &result, nil
}

// DNSStatus returns a snapshot of the daemon's mesh-host DNS state:
// chosen backend, active suffixes, last-applied diff cache, etc.
func (c *Client) DNSStatus() (*DNSStatusResult, error) {
	resp, err := c.Call("dns.status", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}
	var result DNSStatusResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dns status: %w", err)
	}
	return &result, nil
}

// ExposeStatus returns the current state of the expose subsystem.
func (c *Client) ExposeStatus() (*ExposeStatusResult, error) {
	resp, err := c.Call("expose.status", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}
	var result ExposeStatusResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal expose status: %w", err)
	}
	return &result, nil
}

// ExposeAdd installs a runtime forward on the daemon. ftype is
// "folder" or "tcp"; pass path for folder, target for tcp.
func (c *Client) ExposeAdd(key, ftype, path, target, comment string) (*ExposeMutationResult, error) {
	params := ExposeAddParams{
		Key: key, Type: ftype, Path: path, Target: target, Comment: comment,
	}
	resp, err := c.Call("expose.add", params)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}
	var result ExposeMutationResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal expose result: %w", err)
	}
	return &result, nil
}

// ExposeRemove drops a runtime forward.
func (c *Client) ExposeRemove(key string) (*ExposeMutationResult, error) {
	resp, err := c.Call("expose.remove", ExposeRemoveParams{Key: key})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}
	var result ExposeMutationResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal expose result: %w", err)
	}
	return &result, nil
}

// Reload asks the daemon to reload and apply its on-disk config. A nil
// error with Success=false means the daemon rejected the new config
// (e.g. it failed to validate) and kept running the old one.
func (c *Client) Reload() (*ReloadResult, error) {
	resp, err := c.Call("daemon.reload", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}
	var result ReloadResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal reload result: %w", err)
	}
	return &result, nil
}

// Stop asks the daemon to shut down gracefully. The daemon tears down
// its control socket immediately after replying, so a connection reset
// or EOF while reading the response is treated as success.
func (c *Client) Stop() error {
	resp, err := c.Call("daemon.stop", nil)
	if err != nil {
		if isConnClosed(err) {
			return nil
		}
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("API error: %s", resp.Error.Message)
	}
	return nil
}

// Ping is a cheap liveness probe that returns the daemon PID. It is the
// authoritative cross-platform liveness check.
func (c *Client) Ping() (*PingResult, error) {
	resp, err := c.Call("daemon.ping", nil)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s", resp.Error.Message)
	}
	var result PingResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ping result: %w", err)
	}
	return &result, nil
}

// isConnClosed reports whether err is the kind of read failure expected
// when the daemon closes the control connection right after replying to
// a stop request (EOF or a reset).
func isConnClosed(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "closed")
}

// ConfigPath gets the config file path the daemon was started with
func (c *Client) ConfigPath() (string, error) {
	resp, err := c.Call("config.path", nil)
	if err != nil {
		return "", err
	}

	if resp.Error != nil {
		return "", fmt.Errorf("API error: %s", resp.Error.Message)
	}

	var result map[string]string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return result["path"], nil
}
