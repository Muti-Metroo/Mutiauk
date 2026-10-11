package proxy

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Muti-Metroo/Mutiauk/internal/socks5"
)

func TestMapAssociateError_SOCKS5Codes(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		wantV4 byte
		wantV6 byte
	}{
		{
			name:   "command not supported -> admin prohibited",
			err:    &socks5.Error{Code: socks5.ReplyCommandNotSupported, Message: "icmp disabled"},
			wantV4: icmpv4AdminProhibited,
			wantV6: icmpv6AdminProhibited,
		},
		{
			name:   "not allowed -> admin prohibited",
			err:    &socks5.Error{Code: socks5.ReplyNotAllowed, Message: "denied"},
			wantV4: icmpv4AdminProhibited,
			wantV6: icmpv6AdminProhibited,
		},
		{
			name:   "network unreachable -> net unreachable / no route",
			err:    &socks5.Error{Code: socks5.ReplyNetworkUnreachable, Message: "no route"},
			wantV4: icmpv4NetUnreachable,
			wantV6: icmpv6NoRoute,
		},
		{
			name:   "host unreachable -> host / address unreachable",
			err:    &socks5.Error{Code: socks5.ReplyHostUnreachable, Message: "host down"},
			wantV4: icmpv4HostUnreachable,
			wantV6: icmpv6AddressUnreachable,
		},
		{
			name:   "general failure -> host / address unreachable (default)",
			err:    &socks5.Error{Code: socks5.ReplyGeneralFailure, Message: "boom"},
			wantV4: icmpv4HostUnreachable,
			wantV6: icmpv6AddressUnreachable,
		},
		{
			name:   "wrapped socks5 error is still unwrapped",
			err:    fmt.Errorf("icmp associate failed: %w", &socks5.Error{Code: socks5.ReplyCommandNotSupported, Message: "x"}),
			wantV4: icmpv4AdminProhibited,
			wantV6: icmpv6AdminProhibited,
		},
		{
			name:   "non-socks5 error -> host / address unreachable (default)",
			err:    errors.New("dial timeout"),
			wantV4: icmpv4HostUnreachable,
			wantV6: icmpv6AddressUnreachable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapped := mapAssociateError(tt.err)

			var u interface {
				ICMPUnreachableCodes() (byte, byte)
			}
			if !errors.As(mapped, &u) {
				t.Fatalf("mapped error does not expose ICMPUnreachableCodes: %T", mapped)
			}
			v4, v6 := u.ICMPUnreachableCodes()
			if v4 != tt.wantV4 || v6 != tt.wantV6 {
				t.Errorf("codes = (v4=%d, v6=%d), want (v4=%d, v6=%d)", v4, v6, tt.wantV4, tt.wantV6)
			}
			// The reason should preserve the original message.
			if mapped.Error() != tt.err.Error() {
				t.Errorf("reason = %q, want %q", mapped.Error(), tt.err.Error())
			}
		})
	}
}
