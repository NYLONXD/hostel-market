package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"hostelmarket/internal/platform/clock"
)

const (
	mockSignatureHeader = "X-Mock-Signature"
	mockTimestampHeader = "X-Mock-Timestamp"
	mockMaxSkew         = 5 * time.Minute
)

// MockProvider is a development gateway. Payments are "paid" by simulating a webhook,
// which is HMAC-signed exactly like a real provider's so the verification path is
// exercised end to end.
type MockProvider struct {
	secret []byte
	clock  clock.Clock

	mu     sync.Mutex
	status map[string]Status // provider-side view, for GetStatus
}

func NewMockProvider(secret string, clk clock.Clock) *MockProvider {
	return &MockProvider{secret: []byte(secret), clock: clk, status: map[string]Status{}}
}

func (m *MockProvider) Name() string { return "mock" }

func (m *MockProvider) CreatePayment(_ context.Context, _ CreateRequest) (CreateResult, error) {
	ref := "mock_" + uuid.NewString()
	m.setStatus(ref, StatusCreated)
	return CreateResult{ProviderRef: ref, Status: StatusCreated}, nil
}

func (m *MockProvider) GetStatus(_ context.Context, ref string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.status[ref]
	if !ok {
		return StatusPending, nil
	}
	return s, nil
}

func (m *MockProvider) setStatus(ref string, s Status) {
	m.mu.Lock()
	m.status[ref] = s
	m.mu.Unlock()
}

type mockEvent struct {
	EventID     string `json:"event_id"`
	ProviderRef string `json:"provider_ref"`
	Status      Status `json:"status"`
	AmountPaise int64  `json:"amount_paise"`
}

// VerifyWebhook checks HMAC-SHA256(secret, "<timestamp>.<body>") and the timestamp age.
func (m *MockProvider) VerifyWebhook(h http.Header, body []byte) (WebhookEvent, error) {
	ts := h.Get(mockTimestampHeader)
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return WebhookEvent{}, ErrBadSignature
	}
	if age := m.clock.Now().Sub(time.Unix(sec, 0)); age > mockMaxSkew || age < -mockMaxSkew {
		return WebhookEvent{}, fmt.Errorf("%w: timestamp outside the allowed window", ErrBadSignature)
	}
	got, err := hex.DecodeString(h.Get(mockSignatureHeader))
	if err != nil || !hmac.Equal(got, m.sign(ts, body)) {
		return WebhookEvent{}, ErrBadSignature
	}
	var ev mockEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.EventID == "" || ev.ProviderRef == "" {
		return WebhookEvent{}, fmt.Errorf("payments: malformed mock event")
	}
	switch ev.Status {
	case StatusPaid, StatusFailed, StatusPending:
	default:
		return WebhookEvent{}, fmt.Errorf("payments: unknown mock status %q", ev.Status)
	}
	return WebhookEvent{EventID: ev.EventID, ProviderRef: ev.ProviderRef, Status: ev.Status, AmountPaise: ev.AmountPaise, Raw: body}, nil
}

func (m *MockProvider) sign(ts string, body []byte) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return mac.Sum(nil)
}

// SignedWebhook builds a webhook request body and headers as the mock gateway would.
func (m *MockProvider) SignedWebhook(ref string, status Status, amount int64) ([]byte, http.Header) {
	body, _ := json.Marshal(mockEvent{EventID: "evt_" + uuid.NewString(), ProviderRef: ref, Status: status, AmountPaise: amount})
	ts := strconv.FormatInt(m.clock.Now().Unix(), 10)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(mockTimestampHeader, ts)
	h.Set(mockSignatureHeader, hex.EncodeToString(m.sign(ts, body)))
	return body, h
}

// deliver posts a signed webhook to our own endpoint, the way the gateway would.
func (m *MockProvider) deliver(ctx context.Context, url, ref string, status Status, amount int64) error {
	m.setStatus(ref, status)
	body, h := m.SignedWebhook(ref, status, amount)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = h
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("deliver mock webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("deliver mock webhook: status %d", resp.StatusCode)
	}
	return nil
}
