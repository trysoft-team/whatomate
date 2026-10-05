package whatsapp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
)

// FlowEndpointEnvelope is the encrypted body Meta POSTs to a Flow data endpoint.
type FlowEndpointEnvelope struct {
	EncryptedFlowData string `json:"encrypted_flow_data"`
	EncryptedAESKey   string `json:"encrypted_aes_key"`
	InitialVector     string `json:"initial_vector"`
}

// FlowEndpointRequest is the decrypted Flow data endpoint payload.
type FlowEndpointRequest struct {
	Version   string         `json:"version"`
	Action    string         `json:"action"` // ping, INIT, BACK, data_exchange
	Screen    string         `json:"screen,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	FlowToken string         `json:"flow_token,omitempty"`
}

// FlowEndpointSession carries the per-request AES key and IV needed to encrypt
// the response.
type FlowEndpointSession struct {
	aesKey []byte
	iv     []byte
}

// ErrFlowDecrypt means the request could not be decrypted; Meta expects HTTP 421
// so the client re-fetches the business public key.
var ErrFlowDecrypt = errors.New("flow endpoint: cannot decrypt request")

// GenerateFlowKeyPair creates an RSA-2048 key pair for Flow endpoint encryption.
// Returns PEM-encoded PKCS#1 private key and PKIX public key.
func GenerateFlowKeyPair() (privatePEM, publicPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generate RSA key: %w", err)
	}
	privBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal public key: %w", err)
	}
	pubBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return string(privBytes), string(pubBytes), nil
}

func parseRSAPrivateKey(privatePEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privatePEM))
	if block == nil {
		return nil, errors.New("invalid private key PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}

// DecryptFlowRequest decrypts a Flow endpoint envelope (data_api_version 3.0):
// RSA-OAEP-SHA256 for the AES key, AES-128-GCM with a 16-byte IV for the payload.
func DecryptFlowRequest(body []byte, privatePEM string) (*FlowEndpointRequest, *FlowEndpointSession, error) {
	var env FlowEndpointEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFlowDecrypt, err)
	}
	key, err := parseRSAPrivateKey(privatePEM)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFlowDecrypt, err)
	}
	encKey, err1 := base64.StdEncoding.DecodeString(env.EncryptedAESKey)
	encData, err2 := base64.StdEncoding.DecodeString(env.EncryptedFlowData)
	iv, err3 := base64.StdEncoding.DecodeString(env.InitialVector)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFlowDecrypt, err)
	}

	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, encKey, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFlowDecrypt, err)
	}
	gcm, err := newFlowGCM(aesKey, len(iv))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFlowDecrypt, err)
	}
	plain, err := gcm.Open(nil, iv, encData, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFlowDecrypt, err)
	}

	var req FlowEndpointRequest
	if err := json.Unmarshal(plain, &req); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFlowDecrypt, err)
	}
	return &req, &FlowEndpointSession{aesKey: aesKey, iv: iv}, nil
}

// EncryptFlowResponse encrypts a response with the request AES key and the
// bit-inverted IV, returning the base64 body Meta expects (text/plain).
func EncryptFlowResponse(resp any, s *FlowEndpointSession) (string, error) {
	plain, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	flipped := make([]byte, len(s.iv))
	for i, b := range s.iv {
		flipped[i] = b ^ 0xFF
	}
	gcm, err := newFlowGCM(s.aesKey, len(flipped))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nil, flipped, plain, nil)), nil
}

// EncryptFlowRequest performs the WhatsApp client side of the protocol: it
// encrypts a request for the business public key. Used to self-test endpoints.
func EncryptFlowRequest(req any, publicPEM string) (*FlowEndpointEnvelope, *FlowEndpointSession, error) {
	block, _ := pem.Decode([]byte(publicPEM))
	if block == nil {
		return nil, nil, errors.New("invalid public key PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse public key: %w", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, nil, errors.New("public key is not RSA")
	}
	plain, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	aesKey := make([]byte, 16)
	iv := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return nil, nil, err
	}
	if _, err := rand.Read(iv); err != nil {
		return nil, nil, err
	}
	encKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, aesKey, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt AES key: %w", err)
	}
	gcm, err := newFlowGCM(aesKey, len(iv))
	if err != nil {
		return nil, nil, err
	}
	env := &FlowEndpointEnvelope{
		EncryptedFlowData: base64.StdEncoding.EncodeToString(gcm.Seal(nil, iv, plain, nil)),
		EncryptedAESKey:   base64.StdEncoding.EncodeToString(encKey),
		InitialVector:     base64.StdEncoding.EncodeToString(iv),
	}
	return env, &FlowEndpointSession{aesKey: aesKey, iv: iv}, nil
}

// DecryptFlowResponse decrypts an endpoint response produced by EncryptFlowResponse.
func DecryptFlowResponse(body string, s *FlowEndpointSession) (map[string]any, error) {
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	flipped := make([]byte, len(s.iv))
	for i, b := range s.iv {
		flipped[i] = b ^ 0xFF
	}
	gcm, err := newFlowGCM(s.aesKey, len(flipped))
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, flipped, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt response: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return out, nil
}

func newFlowGCM(key []byte, nonceSize int) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}

// BusinessPublicKeyInfo is the public key registered for a phone number.
type BusinessPublicKeyInfo struct {
	BusinessPublicKey                string `json:"business_public_key"`
	BusinessPublicKeySignatureStatus string `json:"business_public_key_signature_status"` // VALID, MISMATCH
}

// SetBusinessPublicKey uploads the Flow endpoint public key for the account's phone number.
func (c *Client) SetBusinessPublicKey(ctx context.Context, account *Account, publicPEM string) error {
	url := fmt.Sprintf("%s/%s/%s/whatsapp_business_encryption", c.getBaseURL(), account.APIVersion, account.PhoneID)
	_, err := c.doRequest(ctx, http.MethodPost, url, map[string]string{"business_public_key": publicPEM}, account.AccessToken)
	if err != nil {
		return fmt.Errorf("upload business public key: %w", err)
	}
	return nil
}

// GetBusinessPublicKey returns the registered public key and its signature status.
func (c *Client) GetBusinessPublicKey(ctx context.Context, account *Account) (*BusinessPublicKeyInfo, error) {
	url := fmt.Sprintf("%s/%s/%s/whatsapp_business_encryption", c.getBaseURL(), account.APIVersion, account.PhoneID)
	body, err := c.doRequest(ctx, http.MethodGet, url, nil, account.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("get business public key: %w", err)
	}
	var resp struct {
		Data []BusinessPublicKeyInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse business public key: %w", err)
	}
	if len(resp.Data) == 0 {
		return &BusinessPublicKeyInfo{}, nil
	}
	return &resp.Data[0], nil
}

// SetFlowEndpointURI sets (or clears, with "") the Flow data endpoint URL on a Meta flow.
func (c *Client) SetFlowEndpointURI(ctx context.Context, account *Account, flowID, endpointURI string) error {
	url := fmt.Sprintf("%s/%s/%s", c.getBaseURL(), account.APIVersion, flowID)
	_, err := c.doRequest(ctx, http.MethodPost, url, map[string]string{"endpoint_uri": endpointURI}, account.AccessToken)
	if err != nil {
		return fmt.Errorf("set flow endpoint_uri: %w", err)
	}
	return nil
}
