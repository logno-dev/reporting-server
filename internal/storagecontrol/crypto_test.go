package storagecontrol

import (
	"bytes"
	"testing"
)

func TestCredentialCipherRoundTrip(t *testing.T) {
	cipher, err := NewCredentialCipher(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"accessKey":"secret"}`)
	encrypted, err := cipher.Encrypt(want, []byte("profile-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, want) {
		t.Fatal("ciphertext contains plaintext credentials")
	}
	got, err := cipher.Decrypt(encrypted, []byte("profile-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Decrypt() = %q, want %q", got, want)
	}
}

func TestCredentialCipherRejectsTampering(t *testing.T) {
	cipher, err := NewCredentialCipher(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt([]byte("secret"), []byte("profile-1"))
	if err != nil {
		t.Fatal(err)
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err := cipher.Decrypt(encrypted, []byte("profile-1")); err == nil {
		t.Fatal("Decrypt() accepted tampered ciphertext")
	}
}

func TestCredentialCipherRejectsWrongContext(t *testing.T) {
	cipher, err := NewCredentialCipher(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt([]byte("secret"), []byte("profile-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Decrypt(encrypted, []byte("profile-2")); err == nil {
		t.Fatal("Decrypt() accepted ciphertext for another profile")
	}
}
