package op

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"golang.org/x/crypto/scrypt"
)

const (
	// EncryptedDBBackupContentType is intentionally distinct from JSON so that
	// browsers and backup tooling do not mistake ciphertext for a plain dump.
	EncryptedDBBackupContentType = "application/vnd.octopus.backup-encrypted"
	EncryptedDBBackupExtension   = ".octopus-backup"

	DBBackupMinPasswordBytes = 8
	DBBackupMaxPasswordBytes = 1024

	dbBackupEnvelopeMagic      = "OCTOBKUP"
	dbBackupEnvelopeVersion    = byte(1)
	dbBackupKDFScrypt          = byte(1)
	dbBackupCipherAES256GCM    = byte(1)
	dbBackupEnvelopeHeaderSize = 36
	dbBackupSaltSize           = 16
	dbBackupNonceSize          = 12
	dbBackupGCMTagSize         = 16
	dbBackupScryptN            = 32768
	dbBackupScryptR            = 8
	dbBackupScryptP            = 1

	// DBBackupEnvelopeOverhead is the exact number of non-plaintext bytes in a
	// version 1 encrypted envelope. Import handlers may accept this many bytes
	// beyond the maximum JSON dump size without weakening the plaintext limit.
	DBBackupEnvelopeOverhead int64 = dbBackupEnvelopeHeaderSize + dbBackupSaltSize + dbBackupNonceSize + dbBackupGCMTagSize
)

var (
	ErrDBBackupTooLarge = errors.New("database backup exceeds size limit")

	ErrDBBackupPasswordRequired = errors.New("encrypted database backup requires a password")
	ErrDBBackupPasswordInvalid  = errors.New("database backup password must be between 8 and 1024 bytes")

	ErrDBBackupInvalidEnvelope      = errors.New("invalid encrypted database backup")
	ErrDBBackupUnsupportedEnvelope  = errors.New("unsupported encrypted database backup format")
	ErrDBBackupUnsupportedKDFParams = errors.New("unsupported encrypted database backup KDF parameters")
	// ErrDBBackupAuthentication deliberately covers both an incorrect password
	// and authenticated-data/ciphertext modification. Callers must not turn it
	// into distinguishable HTTP responses or log supplied passwords.
	ErrDBBackupAuthentication = errors.New("encrypted database backup could not be decrypted")
)

// DBExportAllStreamBounded preserves the table-by-table streaming behavior of
// DBExportAllStream while enforcing an output ceiling. The writer can contain
// a partial JSON document when the limit or an underlying write fails, so HTTP
// callers should treat such a response as an unsuccessful download.
func DBExportAllStreamBounded(ctx context.Context, w io.Writer, includeLogs, includeStats bool, maxBytes int64) error {
	if maxBytes <= 0 {
		return ErrDBBackupTooLarge
	}
	return DBExportAllStream(ctx, &dbBackupLimitWriter{writer: w, remaining: maxBytes}, includeLogs, includeStats)
}

// DBExportAllEncryptedBounded creates a version 1 JSON dump and wraps it in an
// authenticated, versioned binary envelope. AES-GCM is a whole-message AEAD,
// so this function is intentionally not described as streaming: it buffers at
// most maxPlaintextBytes of JSON plus one ciphertext envelope of the same size.
func DBExportAllEncryptedBounded(ctx context.Context, password []byte, includeLogs, includeStats bool, maxPlaintextBytes int64) ([]byte, error) {
	if err := ValidateDBBackupPassword(password); err != nil {
		return nil, err
	}
	var plaintext bytes.Buffer
	if maxPlaintextBytes > 0 && maxPlaintextBytes <= int64(maxInt()) {
		plaintext.Grow(min(int(maxPlaintextBytes), 1<<20))
	}
	if err := DBExportAllStreamBounded(ctx, &plaintext, includeLogs, includeStats, maxPlaintextBytes); err != nil {
		clear(plaintext.Bytes())
		return nil, err
	}
	defer clear(plaintext.Bytes())
	return EncryptDBBackup(plaintext.Bytes(), password, maxPlaintextBytes)
}

// EncryptDBBackup wraps an already encoded plaintext dump. It is exported for
// offline tooling and tests; normal server exports should use
// DBExportAllEncryptedBounded so database serialization is bounded too.
func EncryptDBBackup(plaintext, password []byte, maxPlaintextBytes int64) ([]byte, error) {
	if err := ValidateDBBackupPassword(password); err != nil {
		return nil, err
	}
	if err := validateDBBackupSize(int64(len(plaintext)), maxPlaintextBytes); err != nil {
		return nil, err
	}

	salt := make([]byte, dbBackupSaltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate database backup salt: %w", err)
	}
	nonce := make([]byte, dbBackupNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate database backup nonce: %w", err)
	}

	key, err := scrypt.Key(password, salt, dbBackupScryptN, dbBackupScryptR, dbBackupScryptP, 32)
	if err != nil {
		return nil, fmt.Errorf("derive database backup key: %w", err)
	}
	defer clear(key)
	aead, err := newDBBackupGCM(key)
	if err != nil {
		return nil, err
	}

	prefixSize := dbBackupEnvelopeHeaderSize + len(salt) + len(nonce)
	envelope := make([]byte, prefixSize, prefixSize+len(plaintext)+aead.Overhead())
	copy(envelope, dbBackupEnvelopeMagic)
	envelope[8] = dbBackupEnvelopeVersion
	envelope[9] = dbBackupKDFScrypt
	envelope[10] = dbBackupCipherAES256GCM
	// Byte 11 is a reserved flags byte and must remain zero.
	binary.BigEndian.PutUint32(envelope[12:16], dbBackupScryptN)
	binary.BigEndian.PutUint32(envelope[16:20], dbBackupScryptR)
	binary.BigEndian.PutUint32(envelope[20:24], dbBackupScryptP)
	binary.BigEndian.PutUint16(envelope[24:26], uint16(len(salt)))
	binary.BigEndian.PutUint16(envelope[26:28], uint16(len(nonce)))
	binary.BigEndian.PutUint64(envelope[28:36], uint64(len(plaintext)))
	copy(envelope[dbBackupEnvelopeHeaderSize:], salt)
	copy(envelope[dbBackupEnvelopeHeaderSize+len(salt):], nonce)

	return aead.Seal(envelope, nonce, plaintext, envelope), nil
}

// DecodeDBBackup auto-detects the encrypted envelope. Plain version 1 JSON is
// returned unchanged for backward compatibility. Encrypted input is buffered
// because AES-GCM authenticates the complete message before exposing plaintext;
// maxPlaintextBytes bounds both the accepted length field and allocation.
func DecodeDBBackup(input, password []byte, maxPlaintextBytes int64) ([]byte, error) {
	if !IsEncryptedDBBackup(input) {
		if err := validateDBBackupSize(int64(len(input)), maxPlaintextBytes); err != nil {
			return nil, err
		}
		return input, nil
	}
	if len(password) == 0 {
		return nil, ErrDBBackupPasswordRequired
	}
	if err := ValidateDBBackupPassword(password); err != nil {
		return nil, err
	}
	return decryptDBBackup(input, password, maxPlaintextBytes)
}

func IsEncryptedDBBackup(input []byte) bool {
	return len(input) >= len(dbBackupEnvelopeMagic) && bytes.Equal(input[:len(dbBackupEnvelopeMagic)], []byte(dbBackupEnvelopeMagic))
}

func ValidateDBBackupPassword(password []byte) error {
	if len(password) == 0 {
		return ErrDBBackupPasswordRequired
	}
	if len(password) < DBBackupMinPasswordBytes || len(password) > DBBackupMaxPasswordBytes {
		return ErrDBBackupPasswordInvalid
	}
	return nil
}

func decryptDBBackup(envelope, password []byte, maxPlaintextBytes int64) ([]byte, error) {
	if len(envelope) < dbBackupEnvelopeHeaderSize {
		return nil, ErrDBBackupInvalidEnvelope
	}
	if envelope[8] != dbBackupEnvelopeVersion || envelope[9] != dbBackupKDFScrypt || envelope[10] != dbBackupCipherAES256GCM || envelope[11] != 0 {
		return nil, ErrDBBackupUnsupportedEnvelope
	}
	n := binary.BigEndian.Uint32(envelope[12:16])
	r := binary.BigEndian.Uint32(envelope[16:20])
	p := binary.BigEndian.Uint32(envelope[20:24])
	saltSize := binary.BigEndian.Uint16(envelope[24:26])
	nonceSize := binary.BigEndian.Uint16(envelope[26:28])
	plaintextSize := binary.BigEndian.Uint64(envelope[28:36])

	// Reject attacker-controlled work factors before calling scrypt. Version 1
	// has one supported parameter set; a future version can define another.
	if n != dbBackupScryptN || r != dbBackupScryptR || p != dbBackupScryptP || saltSize != dbBackupSaltSize || nonceSize != dbBackupNonceSize {
		return nil, ErrDBBackupUnsupportedKDFParams
	}
	if maxPlaintextBytes <= 0 || plaintextSize > uint64(maxPlaintextBytes) || plaintextSize > uint64(maxInt()) {
		return nil, ErrDBBackupTooLarge
	}
	prefixSize := dbBackupEnvelopeHeaderSize + int(saltSize) + int(nonceSize)
	if plaintextSize > uint64(maxInt()-prefixSize-dbBackupGCMTagSize) {
		return nil, ErrDBBackupTooLarge
	}
	wantSize := prefixSize + int(plaintextSize) + dbBackupGCMTagSize
	if len(envelope) != wantSize {
		return nil, ErrDBBackupInvalidEnvelope
	}

	salt := envelope[dbBackupEnvelopeHeaderSize : dbBackupEnvelopeHeaderSize+int(saltSize)]
	nonce := envelope[dbBackupEnvelopeHeaderSize+int(saltSize) : prefixSize]
	key, err := scrypt.Key(password, salt, int(n), int(r), int(p), 32)
	if err != nil {
		return nil, ErrDBBackupAuthentication
	}
	defer clear(key)
	aead, err := newDBBackupGCM(key)
	if err != nil {
		return nil, ErrDBBackupAuthentication
	}
	plaintext, err := aead.Open(nil, nonce, envelope[prefixSize:], envelope[:prefixSize])
	if err != nil {
		return nil, ErrDBBackupAuthentication
	}
	return plaintext, nil
}

func newDBBackupGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize database backup cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize database backup AEAD: %w", err)
	}
	return aead, nil
}

func validateDBBackupSize(size, maxBytes int64) error {
	if maxBytes <= 0 || maxBytes > math.MaxInt64-DBBackupEnvelopeOverhead || size < 0 || size > maxBytes {
		return ErrDBBackupTooLarge
	}
	return nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

type dbBackupLimitWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *dbBackupLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, ErrDBBackupTooLarge
	}
	n, err := w.writer.Write(p)
	if n < 0 || n > len(p) {
		return n, fmt.Errorf("database backup writer returned invalid count %d for %d bytes", n, len(p))
	}
	w.remaining -= int64(n)
	if n != len(p) && err == nil {
		return n, io.ErrShortWrite
	}
	return n, err
}
