package op

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

const testDBBackupPassword = "correct horse battery staple"

func TestEncryptedDBBackupRoundTripAndPlaintextCompatibility(t *testing.T) {
	plaintext := []byte(`{"version":1,"exported_at":"2026-07-15T12:00:00Z"}`)
	envelope, err := EncryptDBBackup(plaintext, []byte(testDBBackupPassword), int64(len(plaintext)))
	if err != nil {
		t.Fatalf("EncryptDBBackup() error = %v", err)
	}
	if !IsEncryptedDBBackup(envelope) {
		t.Fatal("encrypted backup magic was not detected")
	}
	if got, want := int64(len(envelope)), int64(len(plaintext))+DBBackupEnvelopeOverhead; got != want {
		t.Fatalf("envelope size = %d, want %d", got, want)
	}
	if envelope[8] != dbBackupEnvelopeVersion || envelope[9] != dbBackupKDFScrypt || envelope[10] != dbBackupCipherAES256GCM {
		t.Fatalf("envelope identifiers = version:%d kdf:%d cipher:%d", envelope[8], envelope[9], envelope[10])
	}

	decrypted, err := DecodeDBBackup(envelope, []byte(testDBBackupPassword), int64(len(plaintext)))
	if err != nil {
		t.Fatalf("DecodeDBBackup(encrypted) error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted backup = %q, want %q", decrypted, plaintext)
	}

	plainDecoded, err := DecodeDBBackup(plaintext, nil, int64(len(plaintext)))
	if err != nil {
		t.Fatalf("DecodeDBBackup(plaintext) error = %v", err)
	}
	if !bytes.Equal(plainDecoded, plaintext) {
		t.Fatalf("plain compatibility changed bytes: got %q want %q", plainDecoded, plaintext)
	}
	if _, err := DecodeDBBackup(envelope, nil, int64(len(plaintext))); !errors.Is(err, ErrDBBackupPasswordRequired) {
		t.Fatalf("DecodeDBBackup(no password) error = %v, want ErrDBBackupPasswordRequired", err)
	}
}

func TestEncryptedDBBackupWrongPasswordAndTamperAreIndistinguishable(t *testing.T) {
	plaintext := []byte(`{"version":1}`)
	envelope, err := EncryptDBBackup(plaintext, []byte(testDBBackupPassword), 1024)
	if err != nil {
		t.Fatalf("EncryptDBBackup() error = %v", err)
	}

	wrongPlaintext, wrongErr := DecodeDBBackup(envelope, []byte("incorrect backup password"), 1024)
	if wrongPlaintext != nil || !errors.Is(wrongErr, ErrDBBackupAuthentication) {
		t.Fatalf("wrong password = (%q, %v), want authentication error", wrongPlaintext, wrongErr)
	}
	tampered := bytes.Clone(envelope)
	tampered[len(tampered)-1] ^= 0x80
	tamperedPlaintext, tamperedErr := DecodeDBBackup(tampered, []byte(testDBBackupPassword), 1024)
	if tamperedPlaintext != nil || !errors.Is(tamperedErr, ErrDBBackupAuthentication) {
		t.Fatalf("tampered ciphertext = (%q, %v), want authentication error", tamperedPlaintext, tamperedErr)
	}
	if wrongErr.Error() != tamperedErr.Error() {
		t.Fatalf("wrong-password error %q differs from tamper error %q", wrongErr, tamperedErr)
	}
}

func TestEncryptedDBBackupRejectsTruncationAndOversize(t *testing.T) {
	plaintext := bytes.Repeat([]byte("x"), 128)
	envelope, err := EncryptDBBackup(plaintext, []byte(testDBBackupPassword), int64(len(plaintext)))
	if err != nil {
		t.Fatalf("EncryptDBBackup() error = %v", err)
	}

	for _, size := range []int{len(dbBackupEnvelopeMagic), dbBackupEnvelopeHeaderSize - 1, len(envelope) - 1} {
		if _, err := DecodeDBBackup(envelope[:size], []byte(testDBBackupPassword), int64(len(plaintext))); !errors.Is(err, ErrDBBackupInvalidEnvelope) {
			t.Errorf("DecodeDBBackup(truncated to %d) error = %v, want ErrDBBackupInvalidEnvelope", size, err)
		}
	}
	if _, err := DecodeDBBackup(envelope, []byte(testDBBackupPassword), int64(len(plaintext)-1)); !errors.Is(err, ErrDBBackupTooLarge) {
		t.Fatalf("DecodeDBBackup(over limit) error = %v, want ErrDBBackupTooLarge", err)
	}
	if _, err := EncryptDBBackup(plaintext, []byte(testDBBackupPassword), int64(len(plaintext)-1)); !errors.Is(err, ErrDBBackupTooLarge) {
		t.Fatalf("EncryptDBBackup(over limit) error = %v, want ErrDBBackupTooLarge", err)
	}
	if _, err := DecodeDBBackup(plaintext, nil, int64(len(plaintext)-1)); !errors.Is(err, ErrDBBackupTooLarge) {
		t.Fatalf("DecodeDBBackup(oversized plaintext) error = %v, want ErrDBBackupTooLarge", err)
	}
}

func TestEncryptedDBBackupRejectsKDFParameterDoSBeforeDerivation(t *testing.T) {
	envelope, err := EncryptDBBackup([]byte(`{"version":1}`), []byte(testDBBackupPassword), 1024)
	if err != nil {
		t.Fatalf("EncryptDBBackup() error = %v", err)
	}

	mutations := map[string]func([]byte){
		"N": func(data []byte) { binary.BigEndian.PutUint32(data[12:16], 1<<30) },
		"r": func(data []byte) { binary.BigEndian.PutUint32(data[16:20], 1<<30) },
		"p": func(data []byte) { binary.BigEndian.PutUint32(data[20:24], 1<<30) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			malicious := bytes.Clone(envelope)
			mutate(malicious)
			if _, err := DecodeDBBackup(malicious, []byte(testDBBackupPassword), 1024); !errors.Is(err, ErrDBBackupUnsupportedKDFParams) {
				t.Fatalf("DecodeDBBackup(malicious %s) error = %v, want ErrDBBackupUnsupportedKDFParams", name, err)
			}
		})
	}

	oversized := bytes.Clone(envelope)
	binary.BigEndian.PutUint64(oversized[28:36], 1<<40)
	if _, err := DecodeDBBackup(oversized, []byte(testDBBackupPassword), 1024); !errors.Is(err, ErrDBBackupTooLarge) {
		t.Fatalf("DecodeDBBackup(malicious size) error = %v, want ErrDBBackupTooLarge", err)
	}
}

func TestDBBackupPasswordAndStreamBounds(t *testing.T) {
	for _, password := range [][]byte{nil, []byte("short"), bytes.Repeat([]byte("p"), DBBackupMaxPasswordBytes+1)} {
		if err := ValidateDBBackupPassword(password); err == nil {
			t.Errorf("ValidateDBBackupPassword(length=%d) error = nil", len(password))
		}
	}
	if err := ValidateDBBackupPassword([]byte("12345678")); err != nil {
		t.Fatalf("ValidateDBBackupPassword(minimum) error = %v", err)
	}

	var output bytes.Buffer
	limited := &dbBackupLimitWriter{writer: &output, remaining: 3}
	if _, err := limited.Write([]byte("abc")); err != nil {
		t.Fatalf("limited exact write error = %v", err)
	}
	if _, err := limited.Write([]byte("d")); !errors.Is(err, ErrDBBackupTooLarge) {
		t.Fatalf("limited overflow error = %v, want ErrDBBackupTooLarge", err)
	}
	if output.String() != "abc" {
		t.Fatalf("limited output = %q, want exact prefix", output.String())
	}

	short := &dbBackupLimitWriter{writer: shortNilDBBackupWriter{}, remaining: 10}
	if n, err := short.Write([]byte("abcd")); n != 2 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("limited short write = (%d, %v), want (2, io.ErrShortWrite)", n, err)
	}
}

type shortNilDBBackupWriter struct{}

func (shortNilDBBackupWriter) Write(p []byte) (int, error) {
	return len(p) / 2, nil
}

func TestDBExportAllEncryptedBoundedProducesImportCompatibleDump(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	if _, err := DBImportRestore(ctx, validDBDump()); err != nil {
		t.Fatalf("seed encrypted export source: %v", err)
	}
	encrypted, err := DBExportAllEncryptedBounded(ctx, []byte(testDBBackupPassword), true, true, 1<<20)
	if err != nil {
		t.Fatalf("DBExportAllEncryptedBounded() error = %v", err)
	}
	plaintext, err := DecodeDBBackup(encrypted, []byte(testDBBackupPassword), 1<<20)
	if err != nil {
		t.Fatalf("DecodeDBBackup(export) error = %v", err)
	}
	var dump model.DBDump
	if err := json.Unmarshal(plaintext, &dump); err != nil {
		t.Fatalf("decode encrypted export JSON: %v", err)
	}
	if dump.Version != dbDumpVersion || dump.ExportedAt.IsZero() || !dump.IncludeLogs || !dump.IncludeStats || len(dump.Channels) != 1 {
		t.Fatalf("encrypted export metadata = %#v", dump)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close encrypted export source: %v", err)
	}
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "encrypted-roundtrip-target.db"), false); err != nil {
		t.Fatalf("initialize encrypted restore target: %v", err)
	}
	result, err := DBImportRestore(ctx, &dump)
	if err != nil {
		t.Fatalf("restore decrypted backup: %v", err)
	}
	if result.Mode != model.DBImportModeEmptyTargetRestore {
		t.Fatalf("encrypted roundtrip mode = %q", result.Mode)
	}
	var channel model.Channel
	if err := db.GetDB().First(&channel, 10).Error; err != nil {
		t.Fatalf("load encrypted-roundtrip channel: %v", err)
	}
	var key model.ChannelKey
	if err := db.GetDB().First(&key, 11).Error; err != nil {
		t.Fatalf("load encrypted-roundtrip key: %v", err)
	}
	if channel.Name != "source-channel" || key.ChannelID != channel.ID || key.ChannelKey != "sk-upstream" {
		t.Fatalf("encrypted roundtrip relation mismatch: channel=%#v key=%#v", channel, key)
	}
}
