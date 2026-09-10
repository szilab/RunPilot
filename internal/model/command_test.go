package model

import "testing"

func TestValidateEnvironment(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
		wantErr     bool
	}{
		{name: "empty is valid", environment: nil},
		{name: "valid names and empty value", environment: map[string]string{"PORT": "", "LOG_LEVEL": "info"}},
		{name: "empty name", environment: map[string]string{"": "value"}, wantErr: true},
		{name: "equals in name", environment: map[string]string{"PORT=HTTP": "8096"}, wantErr: true},
		{name: "case insensitive duplicate", environment: map[string]string{"PATH": "a", "Path": "b"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateEnvironment(test.environment)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateEnvironment() error = %v, want error = %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateCommandWithoutEnvironment(t *testing.T) {
	if err := ValidateCommand(CommandSpec{Path: "C:\\Tools\\app.exe"}); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
}
