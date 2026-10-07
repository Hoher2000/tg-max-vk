package auth

import "testing"

func TestConfig_Validate(t *testing.T) {
	valid := Config{
		APIID:       123,
		APIHash:     "hash",
		Phone:       "+79991234567",
		DBPath:      "./test.db",
		SessionName: "test",
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(c *Config) {}, false},
		{"zero api id", func(c *Config) { c.APIID = 0 }, true},
		{"negative api id", func(c *Config) { c.APIID = -1 }, true},
		{"empty api hash", func(c *Config) { c.APIHash = "" }, true},
		{"empty phone", func(c *Config) { c.Phone = "" }, true},
		{"empty db path", func(c *Config) { c.DBPath = "" }, true},
		{"empty session", func(c *Config) { c.SessionName = "" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("want error=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
