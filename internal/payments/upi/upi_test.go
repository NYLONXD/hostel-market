package upi

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestURI(t *testing.T) {
	uri, err := Link{
		PayeeVPA:    "asha.k@okaxis",
		PayeeName:   "Asha K",
		AmountPaise: 123_450,
		TxnRef:      "5f1c2d3e-aaaa-bbbb-cccc-0123456789ab",
		Note:        "Hostel Market: Hero cycle & lock",
	}.URI()
	require.NoError(t, err)
	assert.Equal(t,
		"upi://pay?pa=asha.k%40okaxis&pn=Asha%20K&am=1234.50&tr=5f1c2d3eaaaabbbbcccc0123456789ab&tn=Hostel%20Market%3A%20Hero%20cycle%20%26%20lock&cu=INR",
		uri)

	u, err := url.Parse(uri)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "asha.k@okaxis", q.Get("pa"))
	assert.Equal(t, "1234.50", q.Get("am"))
	assert.Equal(t, "INR", q.Get("cu"))
	assert.NotContains(t, uri, "+")
}

func TestURITruncatesAndValidates(t *testing.T) {
	uri, err := Link{PayeeVPA: "ab@ybl", AmountPaise: 1, TxnRef: strings.Repeat("x", 60), Note: strings.Repeat("n", 80)}.URI()
	require.NoError(t, err)
	q, _ := url.ParseQuery(strings.TrimPrefix(uri, "upi://pay?"))
	assert.Len(t, q.Get("tr"), 35)
	assert.Len(t, q.Get("tn"), 50)
	assert.Equal(t, "0.01", q.Get("am"))

	bad := []Link{
		{PayeeVPA: "not-a-vpa", AmountPaise: 100, TxnRef: "r1"},
		{PayeeVPA: "ab@ybl", AmountPaise: 0, TxnRef: "r1"},
		{PayeeVPA: "ab@ybl", AmountPaise: 100, TxnRef: "---"},
	}
	for _, l := range bad {
		_, err := l.URI()
		assert.Error(t, err, "%+v", l)
	}
}
