package auth

import (
	"bytes"
	"context"
	"testing"
)

func TestStdinPrompter_ReadCode(t *testing.T) {
	in := bytes.NewBufferString("12345\n")
	out := &bytes.Buffer{}
	p := &StdinPrompter{In: in, Out: out}

	got, err := p.ReadCode(context.Background())
	if err != nil {
		t.Fatalf("ReadCode: %v", err)
	}
	if got != "12345" {
		t.Errorf("want 12345, got %q", got)
	}
	if !bytes.Contains(out.Bytes(), []byte("Код")) {
		t.Errorf("prompt not shown: %q", out.String())
	}
}

func TestStdinPrompter_ReadPassword_FromEnv(t *testing.T) {
	t.Setenv("TG_2FA_PASSWORD", "envpw")

	in := bytes.NewBufferString("") // stdin пустой
	out := &bytes.Buffer{}
	p := &StdinPrompter{In: in, Out: out}

	got, err := p.ReadPassword(context.Background())
	if err != nil {
		t.Fatalf("ReadPassword: %v", err)
	}
	if got != "envpw" {
		t.Errorf("want envpw, got %q", got)
	}
	// Промпт не должен показываться, если пароль взят из env.
	if bytes.Contains(out.Bytes(), []byte("2FA")) {
		t.Errorf("prompt should not be shown when env is set: %q", out.String())
	}
}

func TestStdinPrompter_ReadPassword_Interactive(t *testing.T) {
	t.Setenv("TG_2FA_PASSWORD", "") // чистим

	in := bytes.NewBufferString("mypassword\n")
	out := &bytes.Buffer{}
	p := &StdinPrompter{In: in, Out: out}

	got, err := p.ReadPassword(context.Background())
	if err != nil {
		t.Fatalf("ReadPassword: %v", err)
	}
	if got != "mypassword" {
		t.Errorf("want mypassword, got %q", got)
	}
	if !bytes.Contains(out.Bytes(), []byte("2FA")) {
		t.Errorf("prompt should be shown: %q", out.String())
	}
}