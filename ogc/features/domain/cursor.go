package domain

import (
	"bytes"
	"encoding/base64"
	"log"
	"math/big"
)

const separator = '|'

// Cursors holds next and previous cursor. Note that we use
// 'cursor-based pagination' as opposed to 'offset-based pagination'
type Cursors struct {
	Prev EncodedCursor
	Next EncodedCursor

	HasPrev bool
	HasNext bool
}

// EncodedCursor is a scrambled string representation of the fields defined in DecodedCursor
type EncodedCursor string

// DecodedCursor the cursor values after decoding EncodedCursor
type DecodedCursor struct {
	FID             int64
	FiltersChecksum []byte
}

// PrevNextFID previous and next feature id (fid) to encode in cursor.
type PrevNextFID struct {
	Prev int64
	Next int64
}

// NewCursors create Cursors based on the prev/next feature ids from the datasource
// and the provided filters (captured in a hash).
func NewCursors(fid PrevNextFID, filtersChecksum []byte) Cursors {
	return Cursors{
		Prev: encodeCursor(fid.Prev, filtersChecksum),
		Next: encodeCursor(fid.Next, filtersChecksum),

		HasPrev: fid.Prev > 0,
		HasNext: fid.Next > 0,
	}
}

func encodeCursor(fid int64, filtersChecksum []byte) EncodedCursor {
	fidAsBytes := big.NewInt(fid).Bytes()

	// format of the cursor: <fid><separator><hash>
	cursor := append(fidAsBytes, byte(separator))
	cursor = append(cursor, filtersChecksum...)

	return EncodedCursor(base64.URLEncoding.EncodeToString(cursor))
}

// Decode turns encoded cursor into DecodedCursor and verifies
// the that the checksum of the filter query params hasn't changed
func (c EncodedCursor) Decode(filtersChecksum []byte) DecodedCursor {
	value := string(c)
	if value == "" {
		return DecodedCursor{0, filtersChecksum}
	}

	decoded, err := base64.URLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 {
		log.Printf("decoding cursor value '%v' failed, defaulting to first page", decoded)
		return DecodedCursor{0, filtersChecksum}
	}

	decodedParts := bytes.Split(decoded, []byte{separator})
	if len(decoded) < 1 {
		return DecodedCursor{0, filtersChecksum}
	}

	// feature fid
	fid := big.NewInt(0).SetBytes(decodedParts[0]).Int64()
	if err != nil {
		log.Printf("cursor %s doesn't contain numeric value, defaulting to first page", decodedParts[0])
		return DecodedCursor{0, filtersChecksum}
	}
	if fid < 0 {
		log.Printf("negative feature ID detected: %d, defaulting to first page", fid)
		fid = 0
	}

	// checksum
	if len(decodedParts) > 1 && bytes.Compare(decodedParts[1], filtersChecksum) != 0 {
		log.Printf("filters in query params have changed during pagination, resetting to first page")
		return DecodedCursor{0, filtersChecksum}
	}

	return DecodedCursor{fid, filtersChecksum}
}
