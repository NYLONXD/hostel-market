package orders

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/media"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/testutil"
	"hostelmarket/internal/users"
)

func TestStateMachine(t *testing.T) {
	legal := [][2]Status{
		{StatusRequested, StatusAccepted}, {StatusRequested, StatusRejected}, {StatusRequested, StatusCancelled},
		{StatusRequested, StatusExpired}, {StatusAccepted, StatusAwaitingPayment}, {StatusAccepted, StatusCancelled},
		{StatusAwaitingPayment, StatusPaid}, {StatusAwaitingPayment, StatusExpired}, {StatusAwaitingPayment, StatusCancelled},
		{StatusPaid, StatusHandedOver}, {StatusHandedOver, StatusCompleted},
	}
	for _, p := range legal {
		assert.True(t, CanTransition(p[0], p[1]), "%s -> %s", p[0], p[1])
	}
	illegal := [][2]Status{
		{StatusRequested, StatusPaid}, {StatusAccepted, StatusPaid}, {StatusPaid, StatusCancelled},
		{StatusCompleted, StatusRequested}, {StatusRejected, StatusAccepted}, {StatusExpired, StatusPaid},
		{StatusHandedOver, StatusPaid}, {StatusCancelled, StatusAccepted},
	}
	for _, p := range illegal {
		assert.False(t, CanTransition(p[0], p[1]), "%s -> %s", p[0], p[1])
	}
}

// --- integration ----------------------------------------------------------------

type photosOK struct{}

func (photosOK) VerifyOwned(context.Context, uuid.UUID, []string) ([]media.Asset, error) {
	return nil, nil
}
func (photosOK) ScheduleDelete(context.Context, []string) error { return nil }

type noCache struct{}

func (noCache) Get(context.Context, string, string, any) bool { return false }
func (noCache) Set(context.Context, string, string, any)      {}
func (noCache) Invalidate(context.Context, string)            {}

type recordingJobs struct {
	mu    sync.Mutex
	tasks []*asynq.Task
}

func (r *recordingJobs) Enqueue(_ context.Context, t *asynq.Task, _ ...asynq.Option) error {
	r.mu.Lock()
	r.tasks = append(r.tasks, t)
	r.mu.Unlock()
	return nil
}

type fixture struct {
	pool     *pgxpool.Pool
	users    *users.Service
	listings *listings.Service
	svc      *Service
	clock    *clock.Manual
	jobs     *recordingJobs
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	router := testutil.Router(pool, nil)
	f := &fixture{pool: pool, clock: clock.NewManual(time.Now()), jobs: &recordingJobs{}}
	f.users = users.NewService(router)
	f.listings = listings.NewService(router, f.users, photosOK{}, noCache{}, f.clock)
	f.svc = NewService(router, f.listings, f.users, f.jobs, nil, f.clock, 48*time.Hour)
	f.listings.OnRemove(f.svc)
	return f
}

func (f *fixture) user(t *testing.T, mod ...func(*users.NewUser)) users.User {
	t.Helper()
	upi := "someone@okaxis"
	nu := users.NewUser{Email: uuid.NewString() + "@x.in", PasswordHash: "h", Name: "Student", Hostel: "H1", Block: "A", Year: 2, UPIID: &upi, Verified: true}
	for _, m := range mod {
		m(&nu)
	}
	u, err := f.users.Create(context.Background(), nu)
	require.NoError(t, err)
	return u
}

func (f *fixture) listing(t *testing.T, seller users.User, mod ...func(*listings.Draft)) listings.Listing {
	t.Helper()
	price := int64(150_000)
	d := listings.Draft{Title: "Hero cycle", Category: "cycles", Condition: "good", PricePaise: &price}
	for _, m := range mod {
		m(&d)
	}
	l, err := f.listings.Create(context.Background(), seller.ID, d)
	require.NoError(t, err)
	return l
}

func requireAPIErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr, "got %v", err)
	assert.Equal(t, status, apiErr.Status, apiErr.Message)
	assert.Equal(t, code, apiErr.Code)
}

func (f *fixture) listingStatus(t *testing.T, id uuid.UUID) listings.Status {
	t.Helper()
	l, err := f.listings.GetStrong(context.Background(), id)
	require.NoError(t, err)
	return l.Status
}

// TestOnlyOneBuyerWinsConcurrentAccepts: ten buyers request the same item and the
// seller (say, double-tapping on two devices) accepts all of them at once. The
// FOR UPDATE lock on the listing row must let exactly one through.
func TestOnlyOneBuyerWinsConcurrentAccepts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := f.user(t)
	l := f.listing(t, seller)

	var orderIDs []uuid.UUID
	for range 10 {
		o, err := f.svc.Request(ctx, f.user(t).ID, l.ID, nil)
		require.NoError(t, err)
		orderIDs = append(orderIDs, o.ID)
	}

	var (
		wg       sync.WaitGroup
		accepted atomic.Int32
		reserved atomic.Int32
		other    = make(chan error, len(orderIDs))
		start    = make(chan struct{})
	)
	for _, id := range orderIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.svc.Accept(ctx, seller.ID, id)
			var apiErr *httpx.Error
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.As(err, &apiErr) && apiErr.Code == "listing_reserved":
				reserved.Add(1)
			default:
				other <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(other)
	for err := range other {
		t.Errorf("unexpected error: %v", err)
	}

	assert.EqualValues(t, 1, accepted.Load(), "exactly one buyer gets the item")
	assert.EqualValues(t, 9, reserved.Load())
	assert.Equal(t, listings.StatusReserved, f.listingStatus(t, l.ID))

	var live int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE listing_id = $1 AND status = 'accepted'`, l.ID).Scan(&live))
	assert.Equal(t, 1, live)
}

func TestFixedPriceHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller, buyer, waiting := f.user(t), f.user(t), f.user(t)
	l := f.listing(t, seller)

	note := "  Can pick up tonight  "
	o, err := f.svc.Request(ctx, buyer.ID, l.ID, &note)
	require.NoError(t, err)
	assert.Equal(t, StatusRequested, o.Status)
	assert.Equal(t, "Can pick up tonight", *o.BuyerNote)
	assert.EqualValues(t, 150_000, o.PricePaise)
	assert.Zero(t, o.PlatformFeePaise, "fixed-price sales carry no platform fee")
	assert.EqualValues(t, 150_000, o.SellerPayoutPaise)
	require.Len(t, f.jobs.tasks, 1, "request expiry is scheduled")

	_, err = f.svc.Request(ctx, buyer.ID, l.ID, nil)
	requireAPIErr(t, err, http.StatusConflict, "already_requested")
	other, err := f.svc.Request(ctx, waiting.ID, l.ID, nil)
	require.NoError(t, err)

	_, err = f.svc.Accept(ctx, buyer.ID, o.ID)
	requireAPIErr(t, err, http.StatusForbidden, "wrong_party")

	o, err = f.svc.Accept(ctx, seller.ID, o.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAccepted, o.Status)
	assert.Equal(t, listings.StatusReserved, f.listingStatus(t, l.ID))

	_, err = f.svc.ConfirmPayment(ctx, seller.ID, o.ID)
	requireAPIErr(t, err, http.StatusConflict, "invalid_transition")

	o, err = f.svc.ChoosePaymentMethod(ctx, buyer.ID, o.ID, MethodUPIDirect)
	require.NoError(t, err)
	assert.Equal(t, StatusAwaitingPayment, o.Status)
	pi, err := f.svc.DirectPaymentInstructions(ctx, o)
	require.NoError(t, err)
	assert.Equal(t, "someone@okaxis", pi.PayeeUPIID)
	assert.Contains(t, pi.UPIURI, "upi://pay?pa=someone%40okaxis")
	assert.Contains(t, pi.UPIURI, "am=1500.00")

	o, err = f.svc.ConfirmPayment(ctx, seller.ID, o.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPaid, o.Status)
	assert.Equal(t, listings.StatusSold, f.listingStatus(t, l.ID))

	// Everyone still waiting is turned away once it's sold.
	other, err = f.svc.Get(ctx, waiting.ID, other.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusRejected, other.Status)
	assert.Equal(t, "item sold", *other.CloseReason)

	_, err = f.svc.Complete(ctx, buyer.ID, o.ID)
	requireAPIErr(t, err, http.StatusConflict, "invalid_transition")
	o, err = f.svc.HandOver(ctx, seller.ID, o.ID)
	require.NoError(t, err)
	_, err = f.svc.Complete(ctx, seller.ID, o.ID)
	requireAPIErr(t, err, http.StatusForbidden, "wrong_party")
	o, err = f.svc.Complete(ctx, buyer.ID, o.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, o.Status)
	assert.NotNil(t, o.CompletedAt)
}

func TestCancelReleasesItemAndOutsidersSeeNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller, buyer, stranger := f.user(t), f.user(t), f.user(t)
	l := f.listing(t, seller)

	o, err := f.svc.Request(ctx, buyer.ID, l.ID, nil)
	require.NoError(t, err)

	_, err = f.svc.Get(ctx, stranger.ID, o.ID)
	requireAPIErr(t, err, http.StatusNotFound, "not_found")
	_, err = f.svc.Cancel(ctx, stranger.ID, o.ID, nil)
	requireAPIErr(t, err, http.StatusNotFound, "not_found")
	_, err = f.svc.Cancel(ctx, seller.ID, o.ID, nil)
	requireAPIErr(t, err, http.StatusConflict, "use_reject")

	_, err = f.svc.Accept(ctx, seller.ID, o.ID)
	require.NoError(t, err)
	reason := "changed my mind"
	o, err = f.svc.Cancel(ctx, seller.ID, o.ID, &reason)
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, o.Status)
	assert.Equal(t, listings.StatusActive, f.listingStatus(t, l.ID), "item is back on sale")

	// A new buyer can now get it.
	o2, err := f.svc.Request(ctx, stranger.ID, l.ID, nil)
	require.NoError(t, err)
	_, err = f.svc.Accept(ctx, seller.ID, o2.ID)
	require.NoError(t, err)
}

func TestRequestEnforcesEligibility(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := f.user(t)
	block := "B"
	l := f.listing(t, seller, func(d *listings.Draft) { d.Rules.AllowedBlock = &block })

	_, err := f.svc.Request(ctx, f.user(t).ID, l.ID, nil) // block A
	requireAPIErr(t, err, http.StatusForbidden, "block_restricted")

	_, err = f.svc.Request(ctx, f.user(t, func(u *users.NewUser) { u.Block = "B"; u.Hostel = "H2" }).ID, l.ID, nil)
	requireAPIErr(t, err, http.StatusForbidden, "different_hostel")

	_, err = f.svc.Request(ctx, f.user(t, func(u *users.NewUser) { u.Block = "B"; u.Verified = false }).ID, l.ID, nil)
	requireAPIErr(t, err, http.StatusForbidden, "not_verified")

	_, err = f.svc.Request(ctx, seller.ID, l.ID, nil)
	requireAPIErr(t, err, http.StatusForbidden, "own_listing")

	_, err = f.svc.Request(ctx, f.user(t, func(u *users.NewUser) { u.Block = "b" }).ID, l.ID, nil)
	require.NoError(t, err)

	// Time window: the listing is only open 22:00-06:00 IST.
	night, _ := listings.NewWindow(22*60, 6*60)
	nl := f.listing(t, seller, func(d *listings.Draft) { d.Rules.Window = &night })
	f.clock.Set(time.Date(2026, 10, 21, 14, 0, 0, 0, clock.IST))
	_, err = f.svc.Request(ctx, f.user(t).ID, nl.ID, nil)
	requireAPIErr(t, err, http.StatusForbidden, "outside_time_window")
	f.clock.Set(time.Date(2026, 10, 21, 23, 30, 0, 0, clock.IST))
	_, err = f.svc.Request(ctx, f.user(t).ID, nl.ID, nil)
	require.NoError(t, err)
}

func TestExpireRequestIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller, buyer := f.user(t), f.user(t)
	l := f.listing(t, seller)
	o, err := f.svc.Request(ctx, buyer.ID, l.ID, nil)
	require.NoError(t, err)

	require.NoError(t, f.svc.ExpireRequest(ctx, o.ID), "not due yet: no-op")
	got, _ := f.svc.Get(ctx, buyer.ID, o.ID)
	assert.Equal(t, StatusRequested, got.Status)

	f.clock.Advance(49 * time.Hour)
	_, err = f.svc.Accept(ctx, seller.ID, o.ID)
	requireAPIErr(t, err, http.StatusConflict, "request_expired")

	require.NoError(t, f.svc.ExpireRequest(ctx, o.ID))
	require.NoError(t, f.svc.ExpireRequest(ctx, o.ID), "second run is a no-op")
	got, _ = f.svc.Get(ctx, buyer.ID, o.ID)
	assert.Equal(t, StatusExpired, got.Status)
}

func TestRemovingListingRejectsRequests(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller, buyer := f.user(t), f.user(t)
	l := f.listing(t, seller)
	o, err := f.svc.Request(ctx, buyer.ID, l.ID, nil)
	require.NoError(t, err)

	_, err = f.listings.Remove(ctx, seller.ID, l.ID, false)
	require.NoError(t, err)
	got, err := f.svc.Get(ctx, buyer.ID, o.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusRejected, got.Status)
	assert.Equal(t, "listing removed", *got.CloseReason)
}
