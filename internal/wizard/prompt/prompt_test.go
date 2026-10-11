package prompt

import (
	"strings"
	"testing"
)

func TestReadLineDefault(t *testing.T) {
	SetReader(strings.NewReader("\n"))
	got, err := ReadLine("Name", "fallback")
	if err != nil {
		t.Fatalf("ReadLine error: %v", err)
	}
	if got != "fallback" {
		t.Errorf("ReadLine() = %q, want fallback (empty input uses default)", got)
	}
}

func TestReadLineValue(t *testing.T) {
	SetReader(strings.NewReader("  typed  \n"))
	got, err := ReadLine("Name", "fallback")
	if err != nil {
		t.Fatalf("ReadLine error: %v", err)
	}
	if got != "typed" {
		t.Errorf("ReadLine() = %q, want trimmed 'typed'", got)
	}
}

func TestConfirm(t *testing.T) {
	cases := []struct {
		input      string
		defaultYes bool
		want       bool
	}{
		{"\n", true, true},    // empty accepts default
		{"\n", false, false},  // empty accepts default
		{"y\n", false, true},  // explicit yes overrides default
		{"no\n", true, false}, // explicit no overrides default
		{"YES\n", false, true},
	}
	for _, c := range cases {
		SetReader(strings.NewReader(c.input))
		got, err := Confirm("Proceed?", c.defaultYes)
		if err != nil {
			t.Fatalf("Confirm(%q) error: %v", c.input, err)
		}
		if got != c.want {
			t.Errorf("Confirm(input=%q, default=%v) = %v, want %v", c.input, c.defaultYes, got, c.want)
		}
	}
}

func TestSelectDefaultAndChoice(t *testing.T) {
	opts := []string{"a", "b", "c"}

	SetReader(strings.NewReader("\n")) // empty -> default index
	idx, err := Select("Pick", opts, 2)
	if err != nil {
		t.Fatalf("Select error: %v", err)
	}
	if idx != 2 {
		t.Errorf("Select() default = %d, want 2", idx)
	}

	SetReader(strings.NewReader("1\n")) // explicit first option
	idx, err = Select("Pick", opts, 2)
	if err != nil {
		t.Fatalf("Select error: %v", err)
	}
	if idx != 0 {
		t.Errorf("Select() = %d, want 0", idx)
	}
}

func TestSelectRetriesOnInvalid(t *testing.T) {
	// First an out-of-range number, then a valid one.
	SetReader(strings.NewReader("9\n2\n"))
	idx, err := Select("Pick", []string{"a", "b", "c"}, 0)
	if err != nil {
		t.Fatalf("Select error: %v", err)
	}
	if idx != 1 {
		t.Errorf("Select() = %d, want 1 after retry", idx)
	}
}

func TestReadPasswordNonTerminalUsesReader(t *testing.T) {
	// Under `go test` stdin is not a terminal, so ReadPassword falls
	// back to the shared reader; SetReader must drive it.
	SetReader(strings.NewReader("s3cret\n"))
	got, err := ReadPassword("Password")
	if err != nil {
		t.Fatalf("ReadPassword error: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("ReadPassword() = %q, want s3cret", got)
	}
}
