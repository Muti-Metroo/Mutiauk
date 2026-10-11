package wizard

import (
	"strings"
	"testing"
)

func TestRequired(t *testing.T) {
	validator := Required("username")

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty string fails", "", true},
		{"whitespace only passes", "   ", false}, // Required only checks empty
		{"single char passes", "a", false},
		{"normal string passes", "john_doe", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Required()(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "username") {
				t.Errorf("error should contain field name 'username', got: %v", err)
			}
		})
	}
}

func TestMaxLength(t *testing.T) {
	validator := MaxLength(5)

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty string passes", "", false},
		{"under max passes", "abc", false},
		{"at max passes", "abcde", false},
		{"over max fails", "abcdef", true},
		{"way over max fails", "this is a very long string", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("MaxLength(5)(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestMaxLength_DifferentLimits(t *testing.T) {
	tests := []struct {
		maxLen  int
		input   string
		wantErr bool
	}{
		{0, "", false},
		{0, "a", true},
		{1, "a", false},
		{1, "ab", true},
		{15, "tun-interface01", false}, // TUN interface name limit
		{15, "tun-interface012", true},
		{100, strings.Repeat("a", 100), false},
		{100, strings.Repeat("a", 101), true},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			err := MaxLength(tt.maxLen)(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("MaxLength(%d)(%q) error = %v, wantErr %v", tt.maxLen, tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestIntRange(t *testing.T) {
	validator := IntRange(100, 9000, "MTU")

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{"valid in range", "1500", false, ""},
		{"at minimum", "100", false, ""},
		{"at maximum", "9000", false, ""},
		{"below minimum", "50", true, "must be between"},
		{"above maximum", "10000", true, "must be between"},
		{"non-numeric", "abc", true, "must be a number"},
		{"empty string", "", true, "must be a number"},
		{"float", "1500.5", false, ""}, // Sscanf parses 1500 and stops
		{"negative when min is positive", "-100", true, "must be between"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("IntRange()(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.errMsg != "" && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error should contain %q, got: %v", tt.errMsg, err)
			}
		})
	}
}

func TestIntRange_NegativeRange(t *testing.T) {
	validator := IntRange(-100, 100, "offset")

	tests := []struct {
		input   string
		wantErr bool
	}{
		{"-100", false},
		{"-50", false},
		{"0", false},
		{"50", false},
		{"100", false},
		{"-101", true},
		{"101", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("IntRange(-100,100)(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestYAMLExtension(t *testing.T) {
	validator := YAMLExtension()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"yaml extension", "/etc/mutiauk/config.yaml", false},
		{"yml extension", "/etc/mutiauk/config.yml", false},
		{"YAML uppercase", "/etc/mutiauk/config.YAML", true}, // Case sensitive
		{"json extension", "/etc/mutiauk/config.json", true},
		{"no extension", "/etc/mutiauk/config", true},
		{"too short", "a.yaml"[:4], true}, // "a.ya"
		{"just .yaml", ".yaml", false},
		{"just .yml needs prefix", ".yml", true}, // Too short, needs at least 5 chars
		{"a.yml passes", "a.yml", false},
		{"empty string", "", true},
		{"yamll typo", "config.yamll", true},
		{"hidden yaml file", ".config.yaml", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("YAMLExtension()(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestIPv4CIDR(t *testing.T) {
	validator := IPv4CIDR()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{"valid /8", "10.0.0.0/8", false, ""},
		{"valid /16", "192.168.0.0/16", false, ""},
		{"valid /24", "172.16.1.0/24", false, ""},
		{"valid /32 host", "192.168.1.100/32", false, ""},
		{"valid /0", "0.0.0.0/0", false, ""},
		{"IPv6 fails", "fd00::/8", true, "must be an IPv4"},
		{"IPv6 full fails", "2001:db8::1/128", true, "must be an IPv4"},
		{"invalid CIDR", "10.0.0.0", true, "invalid CIDR"},
		{"invalid format", "not-a-cidr", true, "invalid CIDR"},
		{"empty string", "", true, "invalid CIDR"},
		{"missing mask", "192.168.1.0/", true, "invalid CIDR"},
		{"invalid mask", "192.168.1.0/33", true, "invalid CIDR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("IPv4CIDR()(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.errMsg != "" && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error should contain %q, got: %v", tt.errMsg, err)
			}
		})
	}
}

func TestIPv6CIDR(t *testing.T) {
	validator := IPv6CIDR()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{"valid /8", "fd00::/8", false, ""},
		{"valid /64", "2001:db8::/64", false, ""},
		{"valid /128 host", "2001:db8::1/128", false, ""},
		{"valid /0", "::/0", false, ""},
		{"loopback", "::1/128", false, ""},
		{"IPv4 fails", "10.0.0.0/8", true, "must be an IPv6"},
		{"IPv4 /32 fails", "192.168.1.100/32", true, "must be an IPv6"},
		{"invalid CIDR", "2001:db8::1", true, "invalid CIDR"},
		{"invalid format", "not-a-cidr", true, "invalid CIDR"},
		{"empty string", "", true, "invalid CIDR"},
		{"invalid mask", "2001:db8::/129", true, "invalid CIDR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("IPv6CIDR()(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.errMsg != "" && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error should contain %q, got: %v", tt.errMsg, err)
			}
		})
	}
}

func TestCIDR(t *testing.T) {
	validator := CIDR()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"IPv4 /8", "10.0.0.0/8", false},
		{"IPv4 /24", "192.168.1.0/24", false},
		{"IPv4 /32", "192.168.1.100/32", false},
		{"IPv6 /64", "2001:db8::/64", false},
		{"IPv6 /128", "::1/128", false},
		{"invalid format", "not-a-cidr", true},
		{"missing mask", "10.0.0.0", true},
		{"empty string", "", true},
		{"just slash", "/24", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("CIDR()(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestHostPort(t *testing.T) {
	validator := HostPort()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{"valid localhost", "127.0.0.1:1080", false, ""},
		{"valid hostname", "proxy.example.com:8080", false, ""},
		{"valid IPv6", "[::1]:1080", false, ""},
		{"valid IPv6 full", "[2001:db8::1]:443", false, ""},
		{"missing port", "127.0.0.1", true, "invalid address"},
		{"missing host", ":1080", true, "both host and port"},
		{"empty string", "", true, "invalid address"},
		{"just colon", ":", true, "both host and port"},
		{"port only", ":8080", true, "both host and port"},
		{"no colon", "localhost1080", true, "invalid address"},
		{"double colon IPv4", "127.0.0.1:1080:extra", true, "invalid address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("HostPort()(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.errMsg != "" && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error should contain %q, got: %v", tt.errMsg, err)
			}
		})
	}
}

func TestChain(t *testing.T) {
	t.Run("all validators pass", func(t *testing.T) {
		validator := Chain(
			Required("value"),
			MaxLength(10),
		)
		err := validator("hello")
		if err != nil {
			t.Errorf("Chain() unexpected error: %v", err)
		}
	})

	t.Run("first validator fails", func(t *testing.T) {
		validator := Chain(
			Required("value"),
			MaxLength(10),
		)
		err := validator("")
		if err == nil {
			t.Error("Chain() expected error for empty string")
		}
		if !strings.Contains(err.Error(), "required") {
			t.Errorf("error should be from Required validator, got: %v", err)
		}
	})

	t.Run("second validator fails", func(t *testing.T) {
		validator := Chain(
			Required("value"),
			MaxLength(5),
		)
		err := validator("hello world")
		if err == nil {
			t.Error("Chain() expected error for long string")
		}
		if !strings.Contains(err.Error(), "characters or less") {
			t.Errorf("error should be from MaxLength validator, got: %v", err)
		}
	})

	t.Run("empty chain passes", func(t *testing.T) {
		validator := Chain()
		err := validator("anything")
		if err != nil {
			t.Errorf("empty Chain() should pass, got: %v", err)
		}
	})

	t.Run("single validator", func(t *testing.T) {
		validator := Chain(Required("field"))
		err := validator("")
		if err == nil {
			t.Error("Chain() with single validator should fail for empty string")
		}
	})

	t.Run("three validators", func(t *testing.T) {
		validator := Chain(
			Required("path"),
			MaxLength(100),
			YAMLExtension(),
		)

		// Should pass
		if err := validator("/etc/config.yaml"); err != nil {
			t.Errorf("valid input failed: %v", err)
		}

		// Should fail on Required
		if err := validator(""); err == nil {
			t.Error("empty string should fail")
		}

		// Should fail on YAMLExtension
		if err := validator("/etc/config.json"); err == nil {
			t.Error("json extension should fail")
		}
	})
}

func TestChain_RealWorldExamples(t *testing.T) {
	t.Run("config path validation", func(t *testing.T) {
		validator := Chain(Required("config path"), YAMLExtension())

		tests := []struct {
			input   string
			wantErr bool
		}{
			{"/etc/mutiauk/config.yaml", false},
			{"/etc/mutiauk/config.yml", false},
			{"", true},                         // Required fails
			{"/etc/mutiauk/config.json", true}, // YAMLExtension fails
		}

		for _, tt := range tests {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("config path validator(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		}
	})

	t.Run("TUN interface name validation", func(t *testing.T) {
		validator := Chain(Required("interface name"), MaxLength(15))

		tests := []struct {
			input   string
			wantErr bool
		}{
			{"tun0", false},
			{"tun-mutiauk", false},
			{"tun-interface01", false},       // Exactly 15 chars
			{"", true},                       // Required fails
			{"tun-interface-too-long", true}, // MaxLength fails
		}

		for _, tt := range tests {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("interface name validator(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		}
	})

	t.Run("SOCKS5 server validation", func(t *testing.T) {
		validator := Chain(Required("server address"), HostPort())

		tests := []struct {
			input   string
			wantErr bool
		}{
			{"127.0.0.1:1080", false},
			{"proxy.example.com:8080", false},
			{"", true},          // Required fails
			{"localhost", true}, // HostPort fails (no port)
		}

		for _, tt := range tests {
			err := validator(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("SOCKS5 server validator(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		}
	})
}

// Benchmark tests
func BenchmarkChain(b *testing.B) {
	validator := Chain(
		Required("value"),
		MaxLength(100),
		HostPort(),
	)
	input := "127.0.0.1:1080"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		validator(input)
	}
}

func BenchmarkCIDR(b *testing.B) {
	validator := CIDR()
	input := "10.0.0.0/8"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		validator(input)
	}
}
