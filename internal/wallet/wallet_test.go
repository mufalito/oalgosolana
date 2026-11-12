package wallet

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	secret := bytes.Repeat([]byte{9}, 32) // vault seed pubkey
	master := bytes.Repeat([]byte{7}, 32)
	blob, err := EncryptSecret(secret, master)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(blob, secret) {
		t.Fatal("secret not encrypted")
	}
	got, err := DecryptSecret(blob, master)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("roundtrip mismatch")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	blob, _ := EncryptSecret(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{1}, 32))
	if _, err := DecryptSecret(blob, bytes.Repeat([]byte{2}, 32)); err == nil {
		t.Fatal("wrong master key must fail")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	master := bytes.Repeat([]byte{3}, 32)
	blob, _ := EncryptSecret(bytes.Repeat([]byte{5}, 32), master)
	blob[len(blob)-1] ^= 0xFF
	if _, err := DecryptSecret(blob, master); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestBadMasterKeyLength(t *testing.T) {
	if _, err := EncryptSecret([]byte("x"), []byte("short")); err == nil {
		t.Fatal("short master key must fail")
	}
}
