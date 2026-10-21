package payments

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/media"
	"hostelmarket/internal/orders"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/testutil"
	"hostelmarket/internal/users"
)

// --- unit ---------------------------------------------------------------------------

func TestMockWebhookSignature(t *testing.T) {
	clk := clock.NewManual(time.Unix(1_760_000_000, 0))
	m := NewMockProvider("secret", clk)

	body, h := m.SignedWebhook("mock_ref", StatusPaid, 500)
	ev, err := m.VerifyWebhook(h, body)
	require.NoError(t, err)
	assert.Equal(t, "mock_ref", ev.ProviderRef)
	assert.Equal(t, StatusPaid, ev.Status)
	assert.EqualValues(t, 500, ev.AmountPaise)

	tampered := []byte(strings.Replace(string(body), `"amount_paise":500`, `"amount_paise":1`, 1))
	_, err = m.VerifyWebhook(h, tampered)
	assert.ErrorIs(t, err, ErrBadSignature, "changed body")

	_, err = NewMockProvider("other-secret", clk).VerifyWebhook(h, body)
	assert.ErrorIs(t, err, ErrBadSignature, "wrong secret")

	clk.Advance(10 * time.Minute)
	_, err = m.VerifyWebhook(h, body)
	assert.ErrorIs(t, err, ErrBadSignature, "replayed outside the time window")

	_, err = m.VerifyWebhook(http.Header{}, body)
	assert.ErrorIs(t, err, ErrBadSignature, "unsigned")
}

func TestUPILinkProvider(t *testing.T) {
	p := UPILinkProvider{PayeeVPA: "hostelmarket@okaxis", PayeeName: "Hostel Market"}
	id := uuid.New()
	res, err := p.CreatePayment(context.Background(), CreateRequest{PaymentID: id, AmountPaise: 12_345, Note: "order"})
	require.NoError(t, err)
	q, err := url.ParseQuery(strings.TrimPrefix(res.UPIURI, "upi://pay?"))
	require.NoError(t, err)
	assert.Equal(t, "hostelmarket@okaxis", q.Get("pa"))
	assert.Equal(t, "123.45", q.Get("am"))
	assert.Equal(t, strings.ReplaceAll(id.String(), "-", ""), q.Get("tr"))

	_, err = p.VerifyWebhook(nil, nil)
	assert.ErrorIs(t, err, ErrWebhooksUnsupported)
	_, err = p.GetStatus(context.Background(), res.ProviderRef)
	assert.ErrorIs(t, err, ErrStatusUnsupported)
}

// --- integration ------------------------------------------------------------------

type photosOK struct{}

func (photosOK) VerifyOwned(context.Context, uuid.UUID, []string) ([]media.Asset, error) {
	return nil, nil
}
func (photosOK) ScheduleDelete(context.Context, []string) error { return nil }

type noCache struct{}

func (noCache) Get(context.Context, string, string, any) bool { return false }
func (noCache) Set(context.Context, string, string, any)      {}
func (noCache) Invalidate(context.Context, string)            {}

type nopJobs struct {
	mu    sync.Mutex
	types []string
}

func (n *nopJobs) Enqueue(_ context.Context, t *asynq.Task, _ ...asynq.Option) error {
	n.mu.Lock()
	n.types = append(n.types, t.Type())
	n.mu.Unlock()
	return nil
}

// sqlBidders ranks bids straight from the bids table (stands in for package bidding).
type sqlBidders struct{}

func (sqlBidders) RankedBidders(ctx context.Context, q db.DBTX, listingID uuid.UUID, round int32) ([]orders.Candidate, error) {
	rows, err := q.Query(ctx, `SELECT id, bidder_id, amount_paise FROM bids WHERE listing_id = $1 AND auction_round = $2 ORDER BY amount_paise DESC`, listingID, round)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (orders.Candidate, error) {
		var c orders.Candidate
		return c, r.Scan(&c.BidID, &c.BidderID, &c.AmountPaise)
	})
}

type fixture struct {
	pool     *pgxpool.Pool
	router   *db.Router
	clock    *clock.Manual
	users    *users.Service
	listings *listings.Service
	orders   *orders.Service
	mock     *MockProvider
	svc      *Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	f := &fixture{pool: pool, router: testutil.Router(pool, nil), clock: clock.NewManual(time.Now())}
	f.users = users.NewService(f.router)
	f.listings = listings.NewService(f.router, f.users, photosOK{}, noCache{}, f.clock)
	f.orders = orders.NewService(f.router, f.listings, f.users, &nopJobs{}, nil, f.clock, 48*time.Hour)
	f.orders.EnableAuctions(sqlBidders{}, orders.AuctionConfig{FeeBps: 2000, PaymentDeadline: 30 * time.Minute, MaxPaymentDefaults: 2})
	f.mock = NewMockProvider("whsec", f.clock)
	f.svc = NewService(Options{DB: f.router, Orders: f.orders, Provider: f.mock, Jobs: &nopJobs{}, Clock: f.clock,
		PendingGrace: 30 * time.Minute, WebhookBaseURL: "http://localhost"})
	f.orders.SetPaymentGuard(f.svc)
	return f
}

func (f *fixture) user(t *testing.T) users.User {
	t.Helper()
	upi := "x@okaxis"
	u, err := f.users.Create(context.Background(), users.NewUser{Email: uuid.NewString() + "@x.in", PasswordHash: "h",
		Name: "U", Hostel: "H1", Block: "A", Year: 2, UPIID: &upi, Verified: true})
	require.NoError(t, err)
	return u
}

// wonAuction runs a minimal auction: one bid of amount, closed and awarded.
func (f *fixture) wonAuction(t *testing.T, amount int64) (orders.Order, users.User, users.User) {
	t.Helper()
	ctx := context.Background()
	seller, buyer := f.user(t), f.user(t)
	start, incr, ends := int64(1000), int64(100), f.clock.Now().Add(time.Hour)
	l, err := f.listings.Create(ctx, seller.ID, listings.Draft{Title: "Mini fridge", Category: "appliances", Condition: "good",
		BiddingEnabled: true, StartingPricePaise: &start, MinIncrementPaise: &incr, BidEndsAt: &ends})
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO bids (listing_id, auction_round, bidder_id, amount_paise) VALUES ($1, 1, $2, $3)`, l.ID, buyer.ID, amount)
	require.NoError(t, err)

	var o *orders.Order
	require.NoError(t, f.router.WithTx(ctx, func(tx pgx.Tx) error {
		closed, _, err := f.listings.CloseAuctionRound(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		o, err = f.orders.AwardAuction(ctx, tx, closed)
		return err
	}))
	require.NotNil(t, o)
	return *o, seller, buyer
}

func (f *fixture) deliver(t *testing.T, p Payment, status Status) error {
	t.Helper()
	ref, err := f.providerRef(p.ID)
	require.NoError(t, err)
	body, h := f.mock.SignedWebhook(ref, status, p.AmountPaise)
	return f.svc.HandleWebhook(context.Background(), "mock", h, body)
}

func (f *fixture) providerRef(id uuid.UUID) (string, error) {
	var ref string
	err := f.pool.QueryRow(context.Background(), `SELECT provider_ref FROM payments WHERE id = $1`, id).Scan(&ref)
	return ref, err
}

func requireCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr, "got %v", err)
	assert.Equal(t, status, apiErr.Status, apiErr.Message)
	assert.Equal(t, code, apiErr.Code)
}

func countLedger(t *testing.T, pool *pgxpool.Pool, orderID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE order_id = $1`, orderID).Scan(&n))
	return n
}

func TestCreatePaymentIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o, seller, buyer := f.wonAuction(t, 12_345)

	_, err := f.svc.Create(ctx, buyer.ID, o.ID, "short")
	requireCode(t, err, http.StatusBadRequest, "idempotency_key_required")
	_, err = f.svc.Create(ctx, seller.ID, o.ID, "key-seller-0001")
	requireCode(t, err, http.StatusForbidden, "wrong_party")

	p1, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-0000000001")
	require.NoError(t, err)
	assert.Equal(t, StatusCreated, p1.Status)
	assert.EqualValues(t, 12_345, p1.AmountPaise, "buyer pays the full final price")
	assert.Equal(t, "mock", p1.Provider)

	again, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-0000000001")
	require.NoError(t, err)
	assert.Equal(t, p1.ID, again.ID, "same key, same payment")

	resumed, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-0000000002")
	require.NoError(t, err)
	assert.Equal(t, p1.ID, resumed.ID, "one live payment per order")

	other, _, otherBuyer := f.wonAuction(t, 2000)
	_, err = f.svc.Create(ctx, otherBuyer.ID, other.ID, "key-0000000003")
	require.NoError(t, err)
	_, err = f.svc.Create(ctx, buyer.ID, other.ID, "key-0000000001")
	requireCode(t, err, http.StatusUnprocessableEntity, "idempotency_key_reused")
}

func TestVerifiedWebhookSettlesOrderOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o, seller, buyer := f.wonAuction(t, 12_345)
	p, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-webhook-01")
	require.NoError(t, err)

	// A forged webhook is rejected and changes nothing.
	ref, _ := f.providerRef(p.ID)
	body, h := f.mock.SignedWebhook(ref, StatusPaid, p.AmountPaise)
	h.Set("X-Mock-Signature", strings.Repeat("0", 64))
	requireCode(t, f.svc.HandleWebhook(ctx, "mock", h, body), http.StatusUnauthorized, "invalid_signature")

	body, h = f.mock.SignedWebhook(ref, StatusPaid, p.AmountPaise)
	require.NoError(t, f.svc.HandleWebhook(ctx, "mock", h, body))
	require.NoError(t, f.svc.HandleWebhook(ctx, "mock", h, body), "provider retry of the same event is a no-op")

	got, err := f.svc.Get(ctx, buyer.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPaid, got.Status)

	paidOrder, err := f.orders.Get(ctx, seller.ID, o.ID)
	require.NoError(t, err)
	assert.Equal(t, orders.StatusPaid, paidOrder.Status)
	l, err := f.listings.GetStrong(ctx, o.ListingID)
	require.NoError(t, err)
	assert.Equal(t, listings.StatusSold, l.Status)

	assert.Equal(t, 1, countLedger(t, f.pool, o.ID))
	payouts, _, err := f.svc.ListPayouts(ctx, nil, nil, 10)
	require.NoError(t, err)
	require.Len(t, payouts, 1)
	assert.EqualValues(t, 12_345, payouts[0].GrossPaise)
	assert.EqualValues(t, 2_469, payouts[0].PlatformFeePaise)
	assert.EqualValues(t, 9_876, payouts[0].SellerPayoutPaise)
	assert.Equal(t, "pending", payouts[0].PayoutStatus)

	// A second, distinct "paid" event for the same payment also changes nothing.
	require.NoError(t, f.deliver(t, p, StatusPaid))
	assert.Equal(t, 1, countLedger(t, f.pool, o.ID))
	// So does a late "failed" event.
	require.NoError(t, f.deliver(t, p, StatusFailed))
	got, _ = f.svc.Get(ctx, buyer.ID, p.ID)
	assert.Equal(t, StatusPaid, got.Status)
}

func TestBuyerReportedStatusNeverMarksPaid(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o, _, buyer := f.wonAuction(t, 5_000)
	p, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-utr-000001")
	require.NoError(t, err)

	p, err = f.svc.SubmitUTR(ctx, buyer.ID, p.ID, "412345678901")
	require.NoError(t, err)
	assert.Equal(t, StatusPending, p.Status, "a UTR only makes it pending")
	got, _ := f.orders.Get(ctx, buyer.ID, o.ID)
	assert.Equal(t, orders.StatusAwaitingPayment, got.Status)

	_, err = f.svc.SubmitUTR(ctx, uuid.New(), p.ID, "412345678901")
	requireCode(t, err, http.StatusNotFound, "not_found")

	admin := f.user(t)
	p, err = f.svc.AdminMarkPaid(ctx, admin.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPaid, p.Status)
	got, _ = f.orders.Get(ctx, buyer.ID, o.ID)
	assert.Equal(t, orders.StatusPaid, got.Status)
	assert.Equal(t, 1, countLedger(t, f.pool, o.ID))
}

func TestPayoutOnlyAfterBuyerConfirms(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o, seller, buyer := f.wonAuction(t, 10_000)
	p, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-payout-001")
	require.NoError(t, err)
	require.NoError(t, f.deliver(t, p, StatusPaid))

	payouts, _, err := f.svc.ListPayouts(ctx, nil, nil, 10)
	require.NoError(t, err)
	require.Len(t, payouts, 1)
	assert.False(t, payouts[0].Releasable)

	admin := f.user(t)
	_, err = f.svc.MarkPayoutPaid(ctx, admin.ID, payouts[0].ID, "UPI-REF-1")
	requireCode(t, err, http.StatusConflict, "order_not_completed")

	_, err = f.orders.HandOver(ctx, seller.ID, o.ID)
	require.NoError(t, err)
	_, err = f.orders.Complete(ctx, buyer.ID, o.ID)
	require.NoError(t, err)

	paid, err := f.svc.MarkPayoutPaid(ctx, admin.ID, payouts[0].ID, "UPI-REF-1")
	require.NoError(t, err)
	assert.Equal(t, "paid_out", paid.PayoutStatus)
	assert.Equal(t, "UPI-REF-1", *paid.PayoutReference)
	assert.EqualValues(t, 8_000, paid.SellerPayoutPaise)

	_, err = f.svc.MarkPayoutPaid(ctx, admin.ID, payouts[0].ID, "UPI-REF-2")
	requireCode(t, err, http.StatusConflict, "already_paid_out")
}

func TestPendingPaymentHoldsExpiryUntilGrace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o, _, buyer := f.wonAuction(t, 7_000)
	p, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-hold-00001")
	require.NoError(t, err)
	_, err = f.svc.SubmitUTR(ctx, buyer.ID, p.ID, "UTR123456")
	require.NoError(t, err)

	f.clock.Advance(31 * time.Minute) // past the deadline, inside the grace period
	require.NoError(t, f.orders.ExpireAuctionPayment(ctx, o.ID))
	got, _ := f.orders.Get(ctx, buyer.ID, o.ID)
	assert.Equal(t, orders.StatusAwaitingPayment, got.Status, "held while the UTR awaits verification")

	f.clock.Advance(30 * time.Minute) // grace over
	require.NoError(t, f.orders.ExpireAuctionPayment(ctx, o.ID))
	got, _ = f.orders.Get(ctx, buyer.ID, o.ID)
	assert.Equal(t, orders.StatusExpired, got.Status)
	pay, _ := f.svc.Get(ctx, buyer.ID, p.ID)
	assert.Equal(t, StatusExpired, pay.Status)
}

func TestLatePaymentIsFlaggedForRefund(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o, _, buyer := f.wonAuction(t, 3_000)
	p, err := f.svc.Create(ctx, buyer.ID, o.ID, "key-late-00001")
	require.NoError(t, err)

	f.clock.Advance(31 * time.Minute)
	require.NoError(t, f.orders.ExpireAuctionPayment(ctx, o.ID))

	require.NoError(t, f.deliver(t, p, StatusPaid))
	got, err := f.svc.Get(ctx, buyer.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPaid, got.Status, "the money did arrive")
	require.NotNil(t, got.FailureReason)
	assert.Contains(t, *got.FailureReason, "refund required")

	ord, _ := f.orders.Get(ctx, buyer.ID, o.ID)
	assert.Equal(t, orders.StatusExpired, ord.Status, "an expired order is not revived")
	assert.Zero(t, countLedger(t, f.pool, o.ID), "no payout is owed for a refund case")
}
