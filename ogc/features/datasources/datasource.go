package datasources

import (
	"github.com/PDOK/gokoala/ogc/features/domain"
)

// Datasource holding all the features for a single dataset
type Datasource interface {

	// GetFeatures returns a FeatureCollection from the underlying datasource and a Cursor for pagination
	GetFeatures(collection string, cursor int64, limit int) (*domain.FeatureCollection, domain.Cursor, error)

	// GetFeature returns a specific Feature from the FeatureCollection of the underlying datasource
	GetFeature(collection string, featureID int64) (*domain.Feature, error)

	// Close closes (connections to) the datasource gracefully
	Close()
}
