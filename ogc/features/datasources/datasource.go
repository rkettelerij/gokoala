package datasources

import (
	"context"

	"github.com/PDOK/gokoala/ogc/features/domain"
	"github.com/go-spatial/geom"
)

// Datasource holding all the features for a single dataset
type Datasource interface {

	// GetFeatures returns a FeatureCollection from the underlying datasource and Cursors for pagination
	GetFeatures(ctx context.Context, collection string, options FeatureOptions) (*domain.FeatureCollection, domain.Cursors, error)

	// GetFeature returns a specific Feature from the FeatureCollection of the underlying datasource
	GetFeature(ctx context.Context, collection string, featureID int64) (*domain.Feature, error)

	// Close closes (connections to) the datasource gracefully
	Close()
}

// FeatureOptions to select a certain set of Features
type FeatureOptions struct {
	// pagination
	Cursor domain.DecodedCursor
	Limit  int

	// multiple projections support
	Crs string

	// filtering by bounding box
	Bbox    *geom.Extent
	BboxCrs int

	// filtering by CQL
	Filter    string
	FilterCrs string
}
