package media

import (
	"context"
	"crypto/sha1" //nolint:gosec // Cloudinary's signature scheme is SHA-1
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
)

type fakeCloudinary struct {
	assets  map[string]Asset
	err     error
	deleted [][]string
}

func (f *fakeCloudinary) Asset(_ context.Context, id string) (Asset, error) {
	if f.err != nil {
		return Asset{}, f.err
	}
	a, ok := f.assets[id]
	if !ok {
		return Asset{}, ErrAssetNotFound
	}
	return a, nil
}

func (f *fakeCloudinary) Delete(_ context.Context, ids []string) error {
	f.deleted = append(f.deleted, ids)
	return nil
}

type fakeEnqueuer struct{ tasks []*asynq.Task }

func (f *fakeEnqueuer) Enqueue(_ context.Context, t *asynq.Task, _ ...asynq.Option) error {
	f.tasks = append(f.tasks, t)
	return nil
}

var testCfg = config.CloudinaryConfig{CloudName: "demo", APIKey: "key123", APISecret: "s3cret", Folder: "hm"}

func TestSignUploadMatchesCloudinaryScheme(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	svc := NewService(testCfg, &fakeCloudinary{}, &fakeEnqueuer{}, clock.NewManual(now))
	uid := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	sig, err := svc.SignUpload(uid)
	require.NoError(t, err)
	assert.Equal(t, "hm/users/"+uid.String(), sig.Folder)
	assert.Equal(t, now.Unix(), sig.Timestamp)
	assert.Equal(t, "https://api.cloudinary.com/v1_1/demo/image/upload", sig.UploadURL)
	_, err = uuid.Parse(sig.PublicID)
	assert.NoError(t, err)

	// Cloudinary: sha1 of alphabetically sorted "k=v&..." params followed by the secret.
	toSign := "allowed_formats=" + AllowedFormats + "&folder=" + sig.Folder + "&public_id=" + sig.PublicID +
		"&timestamp=" + strconv.FormatInt(now.Unix(), 10) + testCfg.APISecret
	sum := sha1.Sum([]byte(toSign)) //nolint:gosec
	assert.Equal(t, hex.EncodeToString(sum[:]), sig.Signature)
}

func TestSignUploadDisabledWithoutCloudinary(t *testing.T) {
	svc := NewService(config.CloudinaryConfig{}, nil, &fakeEnqueuer{}, clock.System{})
	_, err := svc.SignUpload(uuid.New())
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 503, apiErr.Status)
}

func TestVerifyOwned(t *testing.T) {
	owner, other := uuid.New(), uuid.New()
	svc := NewService(testCfg, nil, &fakeEnqueuer{}, clock.System{})
	ownerFolder, otherFolder := svc.UserFolder(owner), svc.UserFolder(other)
	cld := &fakeCloudinary{assets: map[string]Asset{
		"fixed-mode":   {PublicID: ownerFolder + "/a", IsImage: true, Bytes: 1000},
		"dynamic-mode": {PublicID: "b", AssetFolder: ownerFolder, IsImage: true, Bytes: 1000},
		"stolen":       {PublicID: otherFolder + "/c", IsImage: true, Bytes: 1000},
		"lookalike":    {PublicID: ownerFolder + "-evil/d", IsImage: true, Bytes: 1000},
		"video":        {PublicID: ownerFolder + "/e", IsImage: false, Bytes: 1000},
		"huge":         {PublicID: ownerFolder + "/f", IsImage: true, Bytes: MaxPhotoBytes + 1},
	}}
	svc.api = cld
	ctx := context.Background()

	assets, err := svc.VerifyOwned(ctx, owner, []string{"fixed-mode", "dynamic-mode"})
	require.NoError(t, err)
	assert.Len(t, assets, 2)

	for _, id := range []string{"stolen", "lookalike", "video", "huge", "missing"} {
		_, err := svc.VerifyOwned(ctx, owner, []string{id})
		var apiErr *httpx.Error
		require.ErrorAs(t, err, &apiErr, id)
		assert.Equal(t, 422, apiErr.Status, id)
	}

	cld.err = errors.New("timeout")
	_, err = svc.VerifyOwned(ctx, owner, []string{"fixed-mode"})
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 502, apiErr.Status)
}

func TestDeleteJobBatchesBy100(t *testing.T) {
	cld := &fakeCloudinary{}
	enq := &fakeEnqueuer{}
	svc := NewService(testCfg, cld, enq, clock.System{})

	ids := make([]string, 205)
	for i := range ids {
		ids[i] = "id" + strconv.Itoa(i)
	}
	require.NoError(t, svc.ScheduleDelete(context.Background(), ids))
	require.Len(t, enq.tasks, 1)

	var p deletePayload
	require.NoError(t, json.Unmarshal(enq.tasks[0].Payload(), &p))
	require.NoError(t, svc.HandleDeleteTask(context.Background(), enq.tasks[0]))
	require.Len(t, cld.deleted, 3)
	assert.Len(t, cld.deleted[0], 100)
	assert.Len(t, cld.deleted[2], 5)

	assert.NoError(t, svc.ScheduleDelete(context.Background(), nil))
	assert.Len(t, enq.tasks, 1, "nothing to delete => no task")
}
