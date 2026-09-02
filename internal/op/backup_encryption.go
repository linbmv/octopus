package op

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"golang.org/x/crypto/scrypt"
)

const (
	EncryptedDBBackupContentType = "application/vnd.octopus.backup-encrypted"
	EncryptedDBBackupExtension   = ".octopus-backup"
	DBBackupMinPasswordBytes     = 8
	DBBackupMaxPasswordBytes     = 1024
	ConfigBackupMaxBytes         = 32 << 20

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
	DBBackupEnvelopeOverhead   = int64(dbBackupEnvelopeHeaderSize + dbBackupSaltSize + dbBackupNonceSize + dbBackupGCMTagSize)
)

var (
	ErrDBBackupPasswordRequired = errors.New("encrypted database backup requires a password")
	ErrDBBackupPasswordInvalid  = errors.New("database backup password must be between 8 and 1024 bytes")
	ErrDBBackupInvalidEnvelope  = errors.New("invalid encrypted database backup")
	ErrDBBackupUnsupported      = errors.New("unsupported encrypted database backup format")
	ErrDBBackupAuthentication   = errors.New("encrypted database backup could not be decrypted")
	ErrDBBackupTooLarge         = errors.New("database backup exceeds size limit")
)

// DBExportConfigEncrypted serializes only configuration fields and encrypts the
// result before it leaves the server. Raw channel/API credentials never travel
// as a downloadable plaintext JSON file.
func DBExportConfigEncrypted(ctx context.Context, password []byte) ([]byte, error) {
	dump, err := DBExportConfig(ctx)
	if err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(dump)
	if err != nil {
		return nil, fmt.Errorf("encode config backup: %w", err)
	}
	defer clear(plaintext)
	return EncryptDBBackup(plaintext, password, ConfigBackupMaxBytes)
}

// DecodeConfigDump decrypts and validates a configuration-only backup.
func DecodeConfigDump(input, password []byte) (*model.ConfigDump, error) {
	if !IsEncryptedDBBackup(input) {
		return nil, ErrDBBackupUnsupported
	}
	plaintext, err := DecodeDBBackup(input, password, ConfigBackupMaxBytes)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)

	var header struct {
		Version int    `json:"version"`
		Scope   string `json:"scope"`
	}
	if err := json.Unmarshal(plaintext, &header); err != nil {
		return nil, ErrDBBackupInvalidEnvelope
	}
	if header.Version == 2 {
		return ConvertEdgeV2Config(plaintext)
	}
	var dump model.ConfigDump
	if err := json.Unmarshal(plaintext, &dump); err != nil {
		return nil, ErrDBBackupInvalidEnvelope
	}
	if dump.Version != configDumpVersion || dump.Scope != model.ConfigDumpScope {
		return nil, ErrDBBackupUnsupported
	}
	return &dump, nil
}

func EncryptDBBackup(plaintext, password []byte, maxPlaintextBytes int64) ([]byte, error) {
	if err := ValidateDBBackupPassword(password); err != nil {
		return nil, err
	}
	if maxPlaintextBytes <= 0 || int64(len(plaintext)) > maxPlaintextBytes {
		return nil, ErrDBBackupTooLarge
	}
	salt := make([]byte, dbBackupSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate backup salt: %w", err)
	}
	nonce := make([]byte, dbBackupNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate backup nonce: %w", err)
	}
	key, err := scrypt.Key(password, salt, dbBackupScryptN, dbBackupScryptR, dbBackupScryptP, 32)
	if err != nil {
		return nil, fmt.Errorf("derive backup key: %w", err)
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

func DecodeDBBackup(input, password []byte, maxPlaintextBytes int64) ([]byte, error) {
	if !IsEncryptedDBBackup(input) {
		return nil, ErrDBBackupUnsupported
	}
	if err := ValidateDBBackupPassword(password); err != nil {
		return nil, err
	}
	if len(input) < dbBackupEnvelopeHeaderSize {
		return nil, ErrDBBackupInvalidEnvelope
	}
	if input[8] != dbBackupEnvelopeVersion || input[9] != dbBackupKDFScrypt || input[10] != dbBackupCipherAES256GCM || input[11] != 0 {
		return nil, ErrDBBackupUnsupported
	}
	n := binary.BigEndian.Uint32(input[12:16])
	r := binary.BigEndian.Uint32(input[16:20])
	p := binary.BigEndian.Uint32(input[20:24])
	saltSize := binary.BigEndian.Uint16(input[24:26])
	nonceSize := binary.BigEndian.Uint16(input[26:28])
	plaintextSize := binary.BigEndian.Uint64(input[28:36])
	if n != dbBackupScryptN || r != dbBackupScryptR || p != dbBackupScryptP || saltSize != dbBackupSaltSize || nonceSize != dbBackupNonceSize {
		return nil, ErrDBBackupUnsupported
	}
	if maxPlaintextBytes <= 0 || plaintextSize > uint64(maxPlaintextBytes) || plaintextSize > uint64(maxInt()) {
		return nil, ErrDBBackupTooLarge
	}
	prefixSize := dbBackupEnvelopeHeaderSize + int(saltSize) + int(nonceSize)
	if plaintextSize > uint64(maxInt()-prefixSize-dbBackupGCMTagSize) || len(input) != prefixSize+int(plaintextSize)+dbBackupGCMTagSize {
		return nil, ErrDBBackupInvalidEnvelope
	}
	salt := input[dbBackupEnvelopeHeaderSize : dbBackupEnvelopeHeaderSize+int(saltSize)]
	nonce := input[dbBackupEnvelopeHeaderSize+int(saltSize) : prefixSize]
	key, err := scrypt.Key(password, salt, int(n), int(r), int(p), 32)
	if err != nil {
		return nil, ErrDBBackupAuthentication
	}
	defer clear(key)
	aead, err := newDBBackupGCM(key)
	if err != nil {
		return nil, ErrDBBackupAuthentication
	}
	plaintext, err := aead.Open(nil, nonce, input[prefixSize:], input[:prefixSize])
	if err != nil {
		return nil, ErrDBBackupAuthentication
	}
	return plaintext, nil
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

func newDBBackupGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize backup cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func maxInt() int { return int(^uint(0) >> 1) }
