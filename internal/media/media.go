// Package media issues Cloudinary signed-upload parameters (clients upload directly to
// Cloudinary, never through us), verifies that submitted assets really belong to the
// caller, and deletes assets in the background.
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudinary/cloudinary-go/v2/api"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"hostelmarket/internal/config"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/jobs"
)

const (
	TaskDeleteAssets = "media:delete_assets"

	// MaxPhotoBytes rejects absurdly large originals when a listing references them.
	MaxPhotoBytes = 10 << 20
	// AllowedFormats is signed into the upload, so Cloudinary itself refuses other types.
	AllowedFormats = "jpg,jpeg,png,webp,heic"
)

var (
	ErrAssetNotFound = errors.New("media: asset not found")
	errDisabled      = httpx.Unavailable("media_unavailable", "photo uploads are not configured on this server")
)

// Asset is what we store about an uploaded photo.
type Asset struct {
	PublicID    string
	SecureURL   string
	AssetFolder string
	Format      string
	Bytes       int
	Width       int
	Height      int
	IsImage     bool
}

// CloudinaryAPI is the part of Cloudinary we call; a fake replaces it in tests.
type CloudinaryAPI interface {
	Asset(ctx context.Context, publicID string) (Asset, error)
	Delete(ctx context.Context, publicIDs []string) error
}

type Service struct {
	cfg   config.CloudinaryConfig
	api   CloudinaryAPI // nil when Cloudinary isn't configured
	jobs  jobs.Enqueuer
	clock clock.Clock
}

func NewService(cfg config.CloudinaryConfig, cld CloudinaryAPI, enq jobs.Enqueuer, clk clock.Clock) *Service {
	return &Service{cfg: cfg, api: cld, jobs: enq, clock: clk}
}

// UserFolder is the only folder a user's signed uploads can land in.
func (s *Service) UserFolder(userID uuid.UUID) string {
	return s.cfg.Folder + "/users/" + userID.String()
}

// UploadSignature is everything the client needs to POST the file to Cloudinary.
type UploadSignature struct {
	UploadURL      string `json:"upload_url"`
	CloudName      string `json:"cloud_name"`
	APIKey         string `json:"api_key"`
	Timestamp      int64  `json:"timestamp"`
	Signature      string `json:"signature"`
	Folder         string `json:"folder"`
	PublicID       string `json:"public_id"`
	AllowedFormats string `json:"allowed_formats"`
}

// SignUpload signs a single upload into the caller's folder. The client must send
// folder, public_id, timestamp, allowed_formats, api_key and signature unchanged.
func (s *Service) SignUpload(userID uuid.UUID) (UploadSignature, error) {
	if s.api == nil {
		return UploadSignature{}, errDisabled
	}
	ts := s.clock.Now().Unix()
	sig := UploadSignature{
		UploadURL:      "https://api.cloudinary.com/v1_1/" + s.cfg.CloudName + "/image/upload",
		CloudName:      s.cfg.CloudName,
		APIKey:         s.cfg.APIKey,
		Timestamp:      ts,
		Folder:         s.UserFolder(userID),
		PublicID:       uuid.NewString(),
		AllowedFormats: AllowedFormats,
	}
	signed, err := api.SignParameters(url.Values{
		"allowed_formats": {sig.AllowedFormats},
		"folder":          {sig.Folder},
		"public_id":       {sig.PublicID},
		"timestamp":       {strconv.FormatInt(ts, 10)},
	}, s.cfg.APISecret)
	if err != nil {
		return UploadSignature{}, httpx.Internal(fmt.Errorf("sign upload: %w", err))
	}
	sig.Signature = signed
	return sig, nil
}

// VerifyOwned checks each public ID exists on Cloudinary, is an image, and sits in the
// caller's folder. Accounts in "fixed folder" mode prefix the public ID with the folder;
// accounts in "dynamic folder" mode report it as asset_folder. Both are accepted.
func (s *Service) VerifyOwned(ctx context.Context, userID uuid.UUID, publicIDs []string) ([]Asset, error) {
	if len(publicIDs) == 0 {
		return nil, nil
	}
	if s.api == nil {
		return nil, errDisabled
	}
	folder := s.UserFolder(userID)
	out := make([]Asset, 0, len(publicIDs))
	for i, id := range publicIDs {
		field := "photo_public_ids[" + strconv.Itoa(i) + "]"
		a, err := s.api.Asset(ctx, id)
		if errors.Is(err, ErrAssetNotFound) {
			return nil, invalidPhoto(field, "photo not found on Cloudinary; upload it first")
		}
		if err != nil {
			return nil, &httpx.Error{Status: 502, Code: "media_upstream_error", Message: "could not verify photos with Cloudinary, try again", Err: err}
		}
		switch {
		case a.AssetFolder != folder && !strings.HasPrefix(a.PublicID, folder+"/"):
			return nil, invalidPhoto(field, "photo was not uploaded by you")
		case !a.IsImage:
			return nil, invalidPhoto(field, "file is not an image")
		case a.Bytes > MaxPhotoBytes:
			return nil, invalidPhoto(field, "photo is larger than 10 MB")
		}
		out = append(out, a)
	}
	return out, nil
}

func invalidPhoto(field, msg string) error {
	return httpx.ValidationFailed(httpx.FieldError{Field: field, Rule: "owned_photo", Message: msg})
}

type deletePayload struct {
	PublicIDs []string `json:"public_ids"`
}

// ScheduleDelete enqueues asset deletion; it retries in the background, so callers
// never block on (or fail because of) Cloudinary.
func (s *Service) ScheduleDelete(ctx context.Context, publicIDs []string) error {
	if len(publicIDs) == 0 {
		return nil
	}
	b, err := json.Marshal(deletePayload{PublicIDs: publicIDs})
	if err != nil {
		return err
	}
	return s.jobs.Enqueue(ctx, asynq.NewTask(TaskDeleteAssets, b), asynq.MaxRetry(10), asynq.Timeout(time.Minute))
}

// HandleDeleteTask is the asynq handler for TaskDeleteAssets. Deleting an
// already-deleted asset is a no-op on Cloudinary, so retries are safe.
func (s *Service) HandleDeleteTask(ctx context.Context, t *asynq.Task) error {
	var p deletePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("%w: bad payload: %w", asynq.SkipRetry, err)
	}
	if s.api == nil {
		slog.WarnContext(ctx, "cloudinary not configured; dropping asset deletion", "count", len(p.PublicIDs))
		return nil
	}
	for start := 0; start < len(p.PublicIDs); start += 100 { // Admin API takes up to 100 per call
		end := min(start+100, len(p.PublicIDs))
		if err := s.api.Delete(ctx, p.PublicIDs[start:end]); err != nil {
			return err
		}
	}
	return nil
}
