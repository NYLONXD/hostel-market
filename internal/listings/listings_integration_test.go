package listings

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/media"
	"hostelmarket/internal/platform/cache"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/testutil"
	"hostelmarket/internal/users"
)

// acceptPhotos treats every public ID as a valid photo owned by the caller.
type acceptPhotos struct {
	mu      sync.Mutex
	deleted []string
}

func (a *acceptPhotos) VerifyOwned(_ context.Context, _ uuid.UUID, ids []string) ([]media.Asset, error) {
	out := make([]media.Asset, len(ids))
	for i, id := range ids {
		out[i] = media.Asset{PublicID: id, SecureURL: "https://img.test/" + id, IsImage: true, Width: 10, Height: 10}
	}
	return out, nil
}

func (a *acceptPhotos) ScheduleDelete(_ context.Context, ids []string) error {
	a.mu.Lock()
	a.deleted = append(a.deleted, ids...)
	a.mu.Unlock()
	return nil
}

type noCache struct{}

func (noCache) Get(context.Context, string, string, any) bool { return false }
func (noCache) Set(context.Context, string, string, any)      {}
func (noCache) Invalidate(context.Context, string)            {}

type fixture struct {
	pool   *pgxpool.Pool
	users  *users.Service
	photos *acceptPhotos
	svc    *Service
	clock  *clock.Manual
}

func newFixture(t *testing.T, feed FeedCache) *fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	router := testutil.Router(pool, nil)
	f := &fixture{pool: pool, users: users.NewService(router), photos: &acceptPhotos{}, clock: clock.NewManual(time.Now())}
	if feed == nil {
		feed = noCache{}
	}
	f.svc = NewService(router, f.users, f.photos, feed, f.clock)
	return f
}

func (f *fixture) user(t *testing.T, hostel string, verified, upi bool) users.User {
	t.Helper()
	nu := users.NewUser{Email: uuid.NewString() + "@x.in", PasswordHash: "h", Name: "U", Hostel: hostel, Block: "A", Year: 2, Verified: verified}
	if upi {
		nu.UPIID = ptr("seller@okaxis")
	}
	u, err := f.users.Create(context.Background(), nu)
	require.NoError(t, err)
	return u
}

func requireCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, status, apiErr.Status)
	assert.Equal(t, code, apiErr.Code)
}

func TestCreateRequiresVerifiedSellerWithUPI(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()

	_, err := f.svc.Create(ctx, f.user(t, "H1", false, true).ID, fixedDraft())
	requireCode(t, err, http.StatusForbidden, "not_verified")

	_, err = f.svc.Create(ctx, f.user(t, "H1", true, false).ID, fixedDraft())
	requireCode(t, err, http.StatusForbidden, "upi_required")

	seller := f.user(t, "H1", true, true)
	d := fixedDraft()
	d.PhotoIDs = []string{"p1", "p2"}
	night, _ := NewWindow(22*60, 6*60)
	d.Rules = Rules{Window: &night, AllowedBlock: ptr(" b "), AllowedYear: ptr[int16](1)}
	l, err := f.svc.Create(ctx, seller.ID, d)
	require.NoError(t, err)
	assert.Equal(t, "H1", l.Hostel, "hostel comes from the seller")
	assert.Equal(t, "B", *l.Rules.AllowedBlock)
	assert.Equal(t, []string{"p1", "p2"}, l.PhotoIDs())

	got, err := f.svc.GetStrong(ctx, l.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Rules.Window)
	assert.Equal(t, "22:00", got.Rules.Window.From.String())
	assert.Equal(t, "06:00", got.Rules.Window.Until.String())

	// A photo can't be attached to two listings.
	other := fixedDraft()
	other.PhotoIDs = []string{"p1"}
	_, err = f.svc.Create(ctx, seller.ID, other)
	requireCode(t, err, http.StatusConflict, "photo_in_use")
}

func TestBrowseScopingSortingAndPagination(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	seller := f.user(t, "H1", true, true)
	viewer := f.user(t, "H1", true, false)

	prices := []int64{500, 300, 900, 100, 700}
	for i, p := range prices {
		d := fixedDraft()
		d.Title = fmt.Sprintf("Item %d", i)
		d.PricePaise = ptr(p)
		if i == 2 {
			d.Title, d.Category, d.Description = "Engineering Mathematics textbook", "books", "Kreyszig 10th edition"
		}
		_, err := f.svc.Create(ctx, seller.ID, d)
		require.NoError(t, err)
	}
	// Another hostel's listing must never appear.
	_, err := f.svc.Create(ctx, f.user(t, "H2", true, true).ID, fixedDraft())
	require.NoError(t, err)

	vctx := context.Background()
	collect := func(fl Filter) []int64 {
		var got []int64
		for {
			page, err := f.svc.Browse(vctx, fl)
			require.NoError(t, err)
			for _, c := range page.Items {
				got = append(got, c.DisplayPricePaise)
			}
			if page.NextCursor == "" {
				return got
			}
			fl.Cursor = page.NextCursor
		}
	}
	base := Filter{Hostel: viewer.Hostel, Sort: SortPriceAsc, Limit: 2}
	assert.Equal(t, []int64{100, 300, 500, 700, 900}, collect(base))

	base.Sort = SortPriceDesc
	assert.Equal(t, []int64{900, 700, 500, 300, 100}, collect(base))

	base.Sort = SortNewest
	assert.Equal(t, []int64{700, 100, 900, 300, 500}, collect(base), "newest first")

	q := "mathematics"
	assert.Equal(t, []int64{900}, collect(Filter{Hostel: "H1", Sort: SortNewest, Limit: 10, Query: &q}), "full-text search stems and matches")
	cat := "books"
	assert.Equal(t, []int64{900}, collect(Filter{Hostel: "H1", Sort: SortNewest, Limit: 10, Category: &cat}))
	assert.Equal(t, []int64{300, 500, 700}, collect(Filter{Hostel: "H1", Sort: SortPriceAsc, Limit: 10, MinPrice: ptr[int64](300), MaxPrice: ptr[int64](700)}))

	// A cursor from one sort order is rejected by another.
	page, err := f.svc.Browse(vctx, Filter{Hostel: "H1", Sort: SortPriceAsc, Limit: 2})
	require.NoError(t, err)
	_, err = f.svc.Browse(vctx, Filter{Hostel: "H1", Sort: SortNewest, Limit: 2, Cursor: page.NextCursor})
	requireCode(t, err, http.StatusBadRequest, "invalid_cursor")
}

func TestUpdateAndRemove(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	seller := f.user(t, "H1", true, true)
	d := fixedDraft()
	d.PhotoIDs = []string{"a", "b", "c"}
	l, err := f.svc.Create(ctx, seller.ID, d)
	require.NoError(t, err)

	_, err = f.svc.Update(ctx, f.user(t, "H1", true, true).ID, l.ID, Patch{Title: ptr("Mine now")})
	requireCode(t, err, http.StatusForbidden, "not_owner")

	newPhotos := []string{"c", "d"}
	l, err = f.svc.Update(ctx, seller.ID, l.ID, Patch{Title: ptr("Cycle, barely used"), PricePaise: ptr[int64](200_000), PhotoIDs: &newPhotos})
	require.NoError(t, err)
	assert.Equal(t, "Cycle, barely used", l.Title)
	assert.EqualValues(t, 200_000, *l.PricePaise)
	assert.Equal(t, []string{"c", "d"}, l.PhotoIDs())
	assert.ElementsMatch(t, []string{"a", "b"}, f.photos.deleted, "dropped photos are deleted from Cloudinary")

	_, err = f.svc.Update(ctx, seller.ID, l.ID, Patch{StartingPricePaise: ptr[int64](1)})
	requireCode(t, err, http.StatusUnprocessableEntity, "validation_failed")

	removed, err := f.svc.Remove(ctx, seller.ID, l.ID, false)
	require.NoError(t, err)
	assert.Equal(t, StatusRemoved, removed.Status)
	assert.ElementsMatch(t, []string{"a", "b", "c", "d"}, f.photos.deleted)

	page, err := f.svc.Browse(ctx, Filter{Hostel: "H1", Sort: SortNewest, Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	_, err = f.svc.Update(ctx, seller.ID, l.ID, Patch{Title: ptr("Back again")})
	requireCode(t, err, http.StatusConflict, "listing_not_editable")
}

func TestAuctionSettingsLockAndNewRound(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	seller := f.user(t, "H1", true, true)
	l, err := f.svc.Create(ctx, seller.ID, auctionDraft2(f.clock.Now()))
	require.NoError(t, err)

	// Simulate a first bid in the open round.
	_, err = f.pool.Exec(ctx, `UPDATE listings SET first_bid_at = now(), current_bid_paise = 120000, bid_count = 1 WHERE id = $1`, l.ID)
	require.NoError(t, err)
	_, err = f.svc.Update(ctx, seller.ID, l.ID, Patch{MinIncrementPaise: ptr[int64](10_000)})
	requireCode(t, err, http.StatusConflict, "bidding_locked")
	_, err = f.svc.Remove(ctx, seller.ID, l.ID, false)
	requireCode(t, err, http.StatusConflict, "auction_in_progress")

	// Non-bidding fields stay editable.
	_, err = f.svc.Update(ctx, seller.ID, l.ID, Patch{Description: ptr("Comes with a case")})
	require.NoError(t, err)

	// After the round closes without a sale, new settings start a fresh round.
	_, err = f.pool.Exec(ctx, `UPDATE listings SET auction_closed_at = now() WHERE id = $1`, l.ID)
	require.NoError(t, err)
	l, err = f.svc.Update(ctx, seller.ID, l.ID, Patch{BidEndsAt: ptr(f.clock.Now().Add(48 * time.Hour))})
	require.NoError(t, err)
	assert.EqualValues(t, 2, l.Auction.Round)
	assert.Nil(t, l.Auction.ClosedAt)
	assert.Nil(t, l.Auction.FirstBidAt)
	assert.Nil(t, l.Auction.CurrentBidPaise)
	assert.Zero(t, l.Auction.BidCount)

	// Admins can remove auctions regardless.
	_, err = f.svc.Remove(ctx, uuid.New(), l.ID, true)
	require.NoError(t, err)
}

func auctionDraft2(now time.Time) Draft {
	d := auctionDraft()
	d.BidEndsAt = ptr(now.Add(24 * time.Hour))
	return d
}

func TestFeedCacheInvalidatedOnWrite(t *testing.T) {
	rdb := testutil.Redis(t)
	feed := cache.NewVersioned(rdb, time.Minute, 0)
	f := newFixture(t, feed)
	ctx := context.Background()
	hostel := "H-" + uuid.NewString()[:8] // unique namespace per run
	seller := f.user(t, hostel, true, true)
	filter := Filter{Hostel: users.NormalizeLabel(hostel), Sort: SortNewest, Limit: 10}

	_, err := f.svc.Create(ctx, seller.ID, fixedDraft())
	require.NoError(t, err)
	page, err := f.svc.Browse(ctx, filter)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	// Insert behind the service's back: the cached page must still be served...
	_, err = f.pool.Exec(ctx, `INSERT INTO listings (seller_id, hostel, title, category, condition, price_paise)
		VALUES ($1, $2, 'Sneaky', 'other', 'good', 100)`, seller.ID, filter.Hostel)
	require.NoError(t, err)
	page, err = f.svc.Browse(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "served from cache")

	// ...until a write through the service invalidates the hostel's feed.
	_, err = f.svc.Create(ctx, seller.ID, fixedDraft())
	require.NoError(t, err)
	page, err = f.svc.Browse(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, page.Items, 3)
}
