package users

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/platform/testutil"
)

func TestNormalize(t *testing.T) {
	assert.Equal(t, "a@b.ac.in", NormalizeEmail("  A@B.ac.IN "))
	assert.Equal(t, "BLOCK B", NormalizeLabel(" block   b "))
	assert.Equal(t, "", NormalizeLabel("   "))
}

func TestUpdateProfile(t *testing.T) {
	svc := NewService(testutil.Router(testutil.Postgres(t), nil))
	ctx := context.Background()

	u, err := svc.Create(ctx, NewUser{Email: "p@x.in", PasswordHash: "h", Name: "P", Hostel: "h1", Block: "a", Year: 1})
	require.NoError(t, err)
	assert.Nil(t, u.UPIID)
	assert.False(t, u.CanSell())

	upi := "p@okaxis"
	block := " c "
	u, err = svc.UpdateProfile(ctx, u.ID, ProfilePatch{Block: &block, SetUPIID: true, UPIID: &upi})
	require.NoError(t, err)
	assert.Equal(t, "C", u.Block)
	require.NotNil(t, u.UPIID)
	assert.Equal(t, upi, *u.UPIID)
	assert.Equal(t, "P", u.Name, "untouched fields keep their value")

	u, err = svc.UpdateProfile(ctx, u.ID, ProfilePatch{SetUPIID: true, UPIID: nil})
	require.NoError(t, err)
	assert.Nil(t, u.UPIID, "explicit null clears the UPI ID")

	_, err = svc.Create(ctx, NewUser{Email: "P@X.in", PasswordHash: "h", Name: "Dup", Hostel: "h1", Block: "a", Year: 1})
	assert.ErrorIs(t, err, ErrEmailTaken)
}
