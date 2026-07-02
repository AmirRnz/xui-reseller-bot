package handlers

import (
	"testing"
)

func TestFormatMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "bold conversion",
			input:    "**bold text**",
			expected: "*bold text*",
		},
		{
			name:     "bold with unescaped underscore",
			input:    "**bold** with _underscore_",
			expected: "*bold* with \\_underscore\\_",
		},
		{
			name:     "bold with unescaped asterisk",
			input:    "**bold** with *single*",
			expected: "*bold* with \\*single\\*",
		},
		{
			name:     "code span preservation",
			input:    "`code_span_with_underscore` and **bold**",
			expected: "`code_span_with_underscore` and *bold*",
		},
		{
			name:     "code block preservation",
			input:    "```\ncode_block_with_*\n``` and **bold**",
			expected: "```\ncode_block_with_*\n``` and *bold*",
		},
		{
			name:     "brackets escaping",
			input:    "some [brackets] here",
			expected: "some \\[brackets] here",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := FormatMarkdown(tc.input)
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		})
	}
}

func TestApplyIPLimitFactor(t *testing.T) {
	tests := []struct {
		name     string
		ipLimit  int
		factor   string
		expected int
	}{
		{"empty factor", 2, "", 2},
		{"invalid format", 2, "abc", 2},
		{"multiplier *2", 2, "*2", 4},
		{"multiplier *3", 3, "*3", 9},
		{"multiplier *0", 2, "*0", 2},
		{"addition +3", 2, "+3", 5},
		{"addition +0", 2, "+0", 2},
		{"addition -3 invalid sign", 2, "-3", 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ApplyIPLimitFactor(tc.ipLimit, tc.factor)
			if res != tc.expected {
				t.Errorf("ApplyIPLimitFactor(%d, %q) = %d; expected %d", tc.ipLimit, tc.factor, res, tc.expected)
			}
		})
	}
}

func TestReverseIPLimitFactor(t *testing.T) {
	tests := []struct {
		name     string
		adjusted int
		factor   string
		expected int
	}{
		{"empty factor", 4, "", 4},
		{"invalid format", 4, "abc", 4},
		{"multiplier *2", 4, "*2", 2},
		{"multiplier *2 division round down", 5, "*2", 2},
		{"multiplier *2 underflow limit", 1, "*2", 1},
		{"addition +3", 5, "+3", 2},
		{"addition +5 underflow limit", 3, "+5", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ReverseIPLimitFactor(tc.adjusted, tc.factor)
			if res != tc.expected {
				t.Errorf("ReverseIPLimitFactor(%d, %q) = %d; expected %d", tc.adjusted, tc.factor, res, tc.expected)
			}
		})
	}
}
