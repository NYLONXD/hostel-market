package media

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudinary/cloudinary-go/v2"
	"github.com/cloudinary/cloudinary-go/v2/api"
	"github.com/cloudinary/cloudinary-go/v2/api/admin"

	"hostelmarket/internal/config"
)

// cloudinaryClient adapts the official SDK to CloudinaryAPI.
type cloudinaryClient struct {
	cld *cloudinary.Cloudinary
}

// NewCloudinary returns nil (media disabled) when credentials are missing.
func NewCloudinary(cfg config.CloudinaryConfig) (CloudinaryAPI, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	cld, err := cloudinary.NewFromParams(cfg.CloudName, cfg.APIKey, cfg.APISecret)
	if err != nil {
		return nil, fmt.Errorf("cloudinary: %w", err)
	}
	cld.Config.URL.Secure = true
	return &cloudinaryClient{cld: cld}, nil
}

func (c *cloudinaryClient) Asset(ctx context.Context, publicID string) (Asset, error) {
	res, err := c.cld.Admin.Asset(ctx, admin.AssetParams{
		AssetType:    api.Image,
		DeliveryType: api.Upload,
		PublicID:     publicID,
	})
	if err != nil {
		return Asset{}, fmt.Errorf("cloudinary asset %q: %w", publicID, err)
	}
	// The SDK reports API-level failures (404, 420 rate limit...) in res.Error.
	if msg := res.Error.Message; msg != "" {
		if strings.Contains(strings.ToLower(msg), "not found") {
			return Asset{}, ErrAssetNotFound
		}
		return Asset{}, errors.New("cloudinary: " + msg)
	}
	return Asset{
		PublicID:    res.PublicID,
		SecureURL:   res.SecureURL,
		AssetFolder: res.AssetFolder,
		Format:      res.Format,
		Bytes:       res.Bytes,
		Width:       res.Width,
		Height:      res.Height,
		IsImage:     res.ResourceType == "image",
	}, nil
}

func (c *cloudinaryClient) Delete(ctx context.Context, publicIDs []string) error {
	res, err := c.cld.Admin.DeleteAssets(ctx, admin.DeleteAssetsParams{
		AssetType:    api.Image,
		DeliveryType: api.Upload,
		PublicIDs:    publicIDs,
	})
	if err != nil {
		return fmt.Errorf("cloudinary delete: %w", err)
	}
	if msg := res.Error.Message; msg != "" {
		return errors.New("cloudinary delete: " + msg)
	}
	return nil
}
