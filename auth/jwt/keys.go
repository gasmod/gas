package jwt

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// maxKeyFileSize is the largest key file Init reads. A PEM RSA key of
// 16384 bits, far beyond any in use, is about 12 KiB.
const maxKeyFileSize = 64 << 10

// The reasons a key file is rejected. None of them quotes the file.
var (
	// errKeyFileTooLarge reports a key file over maxKeyFileSize.
	errKeyFileTooLarge = errors.New("file is larger than 64 KiB")
	// errNoPEMBlock reports a key file without a PEM block.
	errNoPEMBlock = errors.New("no PEM block found")
	// errNotRSAPublicKey reports a PEM block that is not an RSA public key.
	errNotRSAPublicKey = errors.New("not an RSA public key in PKIX or PKCS #1 form")
	// errNotRSAPrivateKey reports a PEM block that is not an RSA private
	// key.
	errNotRSAPrivateKey = errors.New(
		"not an unencrypted RSA private key in PKCS #8 or PKCS #1 form",
	)
	// errKeyMismatch reports a private key whose public half is not the
	// configured public key.
	errKeyMismatch = errors.New("key does not match JWT.PublicKeyPath")
)

// rsaKeys holds the keys loaded for RS256. private is nil when no private
// key path is set.
type rsaKeys struct {
	public  *rsa.PublicKey
	private *rsa.PrivateKey
}

// loadRSAKeys loads the public key, and the private key when its path is
// set, and checks that they are large enough and belong together.
func loadRSAKeys(s *Settings) (*rsaKeys, error) {
	pub, err := loadKey(s.PublicKeyPath, "JWT.PublicKeyPath", parseRSAPublicKey)
	if err != nil {
		return nil, err
	}

	keys := &rsaKeys{public: pub}

	if s.PrivateKeyPath == "" {
		return keys, nil
	}

	priv, err := loadKey(s.PrivateKeyPath, "JWT.PrivateKeyPath", parseRSAPrivateKey)
	if err != nil {
		return nil, err
	}

	if !priv.PublicKey.Equal(pub) {
		return nil, fmt.Errorf("%w JWT.PrivateKeyPath: %w", ErrKeyLoadFailed, errKeyMismatch)
	}

	keys.private = priv

	return keys, nil
}

// loadKey reads the PEM file at path and parses its first block with
// parse. Its errors name setting and never include the path or the file's
// contents.
func loadKey[K any](path, setting string, parse func([]byte) (K, error)) (K, error) {
	var zero K

	data, err := readKeyFile(path)
	if err != nil {
		return zero, fmt.Errorf("%w %s: %w", ErrKeyLoadFailed, setting, err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return zero, fmt.Errorf("%w %s: %w", ErrKeyLoadFailed, setting, errNoPEMBlock)
	}

	key, err := parse(block.Bytes)
	if err != nil {
		return zero, fmt.Errorf("%w %s: %w", ErrKeyLoadFailed, setting, err)
	}

	return key, nil
}

// readKeyFile reads at most maxKeyFileSize bytes of the file at path. Its
// errors leave out the path, and still match fs.ErrNotExist and the like.
func readKeyFile(path string) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, withoutPath(err)
	}

	defer func() { _ = f.Close() }() // read-only, so Close reports nothing useful

	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize+1))
	if err != nil {
		return nil, withoutPath(err)
	}

	if len(data) > maxKeyFileSize {
		return nil, errKeyFileTooLarge
	}

	return data, nil
}

// withoutPath returns an *fs.PathError as its operation and the error it
// wraps, leaving out the path, or err itself.
func withoutPath(err error) error {
	pe, ok := errors.AsType[*fs.PathError](err)
	if ok {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}

	return err
}

// parseRSAPublicKey parses a PKIX or PKCS #1 RSA public key of at least
// MinRSAKeyBits. The parsers' errors are replaced, since they may quote
// bytes of the key.
func parseRSAPublicKey(der []byte) (*rsa.PublicKey, error) {
	var pub *rsa.PublicKey

	parsed, err := x509.ParsePKIXPublicKey(der)
	if err == nil {
		var ok bool

		pub, ok = parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errNotRSAPublicKey
		}
	} else {
		pub, err = x509.ParsePKCS1PublicKey(der)
		if err != nil {
			return nil, errNotRSAPublicKey
		}
	}

	err = checkKeySize(pub)
	if err != nil {
		return nil, err
	}

	return pub, nil
}

// parseRSAPrivateKey parses a PKCS #8 or PKCS #1 RSA private key of at
// least MinRSAKeyBits. The parsers' errors are replaced, since they may
// quote bytes of the key.
func parseRSAPrivateKey(der []byte) (*rsa.PrivateKey, error) {
	var priv *rsa.PrivateKey

	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err == nil {
		var ok bool

		priv, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errNotRSAPrivateKey
		}
	} else {
		priv, err = x509.ParsePKCS1PrivateKey(der)
		if err != nil {
			return nil, errNotRSAPrivateKey
		}
	}

	err = checkKeySize(&priv.PublicKey)
	if err != nil {
		return nil, err
	}

	return priv, nil
}

// checkKeySize rejects an RSA key smaller than MinRSAKeyBits. The size of
// a key is not secret.
func checkKeySize(pub *rsa.PublicKey) error {
	bits := pub.N.BitLen()
	if bits < MinRSAKeyBits {
		return fmt.Errorf(
			"%w: RSA key has %d bits, fewer than %d",
			ErrKeyTooSmall,
			bits,
			MinRSAKeyBits,
		)
	}

	return nil
}
