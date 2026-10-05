package whatsapp

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// flowMediaHMACLen is the truncated HMAC-SHA256 appended to WhatsApp CDN files.
const flowMediaHMACLen = 10

// FlowMediaEncryption is the encryption_metadata WhatsApp attaches to every
// PhotoPicker / DocumentPicker file. All values are base64.
type FlowMediaEncryption struct {
	EncryptedHash string `json:"encrypted_hash"`
	IV            string `json:"iv"`
	EncryptionKey string `json:"encryption_key"`
	HMACKey       string `json:"hmac_key"`
	PlaintextHash string `json:"plaintext_hash"`
}

// FlowMedia is one file uploaded through a Flow PhotoPicker / DocumentPicker.
type FlowMedia struct {
	MediaID            string              `json:"media_id"`
	CDNURL             string              `json:"cdn_url"`
	FileName           string              `json:"file_name"`
	EncryptionMetadata FlowMediaEncryption `json:"encryption_metadata"`
}

// ErrFlowMediaIntegrity means a CDN file failed a hash or HMAC check.
var ErrFlowMediaIntegrity = errors.New("flow media integrity check failed")

// ParseFlowMediaList reports whether v is a PhotoPicker / DocumentPicker value
// (a non-empty list of objects with cdn_url and encryption_metadata).
func ParseFlowMediaList(v any) ([]FlowMedia, bool) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		if url, _ := m["cdn_url"].(string); url == "" {
			return nil, false
		}
		if _, ok := m["encryption_metadata"].(map[string]any); !ok {
			return nil, false
		}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return nil, false
	}
	var out []FlowMedia
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// DecryptFlowMedia verifies and decrypts a file downloaded from a Flow media
// cdn_url (AES-256-CBC + truncated HMAC-SHA256 + PKCS7).
func DecryptFlowMedia(cdnFile []byte, meta FlowMediaEncryption) ([]byte, error) {
	encHash, err := base64.StdEncoding.DecodeString(meta.EncryptedHash)
	if err != nil {
		return nil, fmt.Errorf("decode encrypted_hash: %w", err)
	}
	iv, err := base64.StdEncoding.DecodeString(meta.IV)
	if err != nil || len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("invalid iv")
	}
	key, err := base64.StdEncoding.DecodeString(meta.EncryptionKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid encryption_key")
	}
	hmacKey, err := base64.StdEncoding.DecodeString(meta.HMACKey)
	if err != nil || len(hmacKey) == 0 {
		return nil, fmt.Errorf("invalid hmac_key")
	}
	plainHash, err := base64.StdEncoding.DecodeString(meta.PlaintextHash)
	if err != nil {
		return nil, fmt.Errorf("decode plaintext_hash: %w", err)
	}

	sum := sha256.Sum256(cdnFile)
	if !hmac.Equal(sum[:], encHash) {
		return nil, fmt.Errorf("%w: encrypted hash mismatch", ErrFlowMediaIntegrity)
	}
	if len(cdnFile) < flowMediaHMACLen+aes.BlockSize {
		return nil, fmt.Errorf("%w: file too short", ErrFlowMediaIntegrity)
	}
	ciphertext := cdnFile[:len(cdnFile)-flowMediaHMACLen]
	tag := cdnFile[len(cdnFile)-flowMediaHMACLen:]
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("%w: ciphertext not block aligned", ErrFlowMediaIntegrity)
	}

	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(iv)
	mac.Write(ciphertext)
	if !hmac.Equal(mac.Sum(nil)[:flowMediaHMACLen], tag) {
		return nil, fmt.Errorf("%w: hmac mismatch", ErrFlowMediaIntegrity)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	plain, err = pkcs7Unpad(plain)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFlowMediaIntegrity, err)
	}

	sum = sha256.Sum256(plain)
	if !hmac.Equal(sum[:], plainHash) {
		return nil, fmt.Errorf("%w: plaintext hash mismatch", ErrFlowMediaIntegrity)
	}
	return plain, nil
}

// EncryptFlowMedia encrypts plain the way WhatsApp stores Flow uploads on its
// CDN. It is the inverse of DecryptFlowMedia, for tests and self-checks.
func EncryptFlowMedia(plain []byte) ([]byte, FlowMediaEncryption, error) {
	key := make([]byte, 32)
	hmacKey := make([]byte, 32)
	iv := make([]byte, aes.BlockSize)
	for _, b := range [][]byte{key, hmacKey, iv} {
		if _, err := rand.Read(b); err != nil {
			return nil, FlowMediaEncryption{}, err
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, FlowMediaEncryption{}, err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(iv)
	mac.Write(ciphertext)
	cdnFile := append(ciphertext, mac.Sum(nil)[:flowMediaHMACLen]...)

	encHash := sha256.Sum256(cdnFile)
	plainHash := sha256.Sum256(plain)
	b64 := base64.StdEncoding.EncodeToString
	return cdnFile, FlowMediaEncryption{
		EncryptedHash: b64(encHash[:]),
		IV:            b64(iv),
		EncryptionKey: b64(key),
		HMACKey:       b64(hmacKey),
		PlaintextHash: b64(plainHash[:]),
	}, nil
}

// DownloadFlowMedia fetches an encrypted Flow upload from the WhatsApp CDN.
// CDN URLs are pre-signed, so no access token is sent.
func (c *Client) DownloadFlowMedia(ctx context.Context, cdnURL string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdnURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create flow media request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download flow media: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flow media download failed with status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read flow media: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("flow media larger than %d bytes", maxBytes)
	}
	return data, nil
}

func pkcs7Pad(b []byte, size int) []byte {
	n := size - len(b)%size
	return append(append([]byte{}, b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%aes.BlockSize != 0 {
		return nil, errors.New("invalid padded length")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, errors.New("invalid padding")
	}
	for _, p := range b[len(b)-n:] {
		if int(p) != n {
			return nil, errors.New("invalid padding")
		}
	}
	return b[:len(b)-n], nil
}
