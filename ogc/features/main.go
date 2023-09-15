package features

import (
	"net/http"

	"github.com/PDOK/gokoala/engine"
	"github.com/PDOK/gokoala/ogc/common/geospatial"
	"github.com/PDOK/gokoala/ogc/features/datasources"
	"github.com/paulmach/orb/geojson"

	"github.com/go-chi/chi/v5"
)

const (
	templatesDir = "ogc/features/templates/"
)

var (
	collectionsBreadcrumb = []engine.Breadcrumb{
		{
			Name: "Collections",
			Path: "collections",
		},
	}
	collectionsMetadata map[string]*engine.GeoSpatialCollectionMetadata
)

type Features struct {
	engine     *engine.Engine
	datasource datasources.Datasource
}

func NewFeatures(e *engine.Engine, router *chi.Mux) *Features {
	var datasource datasources.Datasource
	if e.Config.OgcAPI.Features.Datasource.FakeDB {
		datasource = datasources.NewFakeDB()
	} else if e.Config.OgcAPI.Features.Datasource.GeoPackage != nil {
		datasource = datasources.NewGeoPackage()
	}
	e.RegisterShutdownHook(datasource.Close)

	features := &Features{
		engine:     e,
		datasource: datasource,
	}
	collectionsMetadata = features.cacheCollectionsMetadata()

	router.Get(geospatial.CollectionsPath+"/{collectionId}/items", features.CollectionContent())
	router.Get(geospatial.CollectionsPath+"/{collectionId}/items/{featureId}", features.Feature())
	return features
}

// CollectionContent serve a FeatureCollection with the given collectionId
func (f *Features) CollectionContent() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		collectionID := chi.URLParam(r, "collectionId")

		fc := f.datasource.GetFeatures(collectionID)
		if fc == nil {
			http.NotFound(w, r)
			return
		}

		format := f.engine.CN.NegotiateFormat(r)
		switch format {
		case engine.FormatHTML:
			collectionMetadata := collectionsMetadata[collectionID]

			breadcrumbs := collectionsBreadcrumb
			breadcrumbs = append(breadcrumbs, []engine.Breadcrumb{
				{
					Name: f.getCollectionTitle(collectionID, collectionMetadata),
					Path: "collections/" + collectionID,
				},
				{
					Name: "Items",
					Path: "collections/" + collectionID + "/items",
				},
			}...)

			pageContent := &featureCollectionPage{
				fc,
				collectionID,
				collectionMetadata,
			}

			lang := f.engine.CN.NegotiateLanguage(w, r)
			key := engine.NewTemplateKeyWithLanguage(templatesDir+"features.go."+format, lang)
			f.engine.RenderAndServePage(w, r, pageContent, breadcrumbs, key, lang)
		case engine.FormatJSON:
			fcJSON, err := fc.MarshalJSON()
			if err != nil {
				http.Error(w, "Failed to marshal FeatureCollection to JSON", http.StatusInternalServerError)
				return
			}
			engine.SafeWrite(w.Write, fcJSON)
		default:
			http.NotFound(w, r)
		}
	}
}

// Feature serves a single Feature
func (f *Features) Feature() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		collectionID := chi.URLParam(r, "collectionId")
		featureID := chi.URLParam(r, "featureId")

		feat := f.datasource.GetFeature(collectionID, featureID)
		if feat == nil {
			http.NotFound(w, r)
			return
		}

		format := f.engine.CN.NegotiateFormat(r)
		switch format {
		case engine.FormatHTML:
			collectionMetadata := collectionsMetadata[collectionID]

			breadcrumbs := collectionsBreadcrumb
			breadcrumbs = append(breadcrumbs, []engine.Breadcrumb{
				{
					Name: f.getCollectionTitle(collectionID, collectionMetadata),
					Path: "collections/" + collectionID,
				},
				{
					Name: "Items",
					Path: "collections/" + collectionID + "/items",
				},
				{
					Name: featureID,
					Path: "collections/" + collectionID + "/items/" + featureID,
				},
			}...)

			pageContent := &featurePage{
				feat,
				featureID,
				collectionMetadata,
			}

			lang := f.engine.CN.NegotiateLanguage(w, r)
			key := engine.NewTemplateKeyWithLanguage(templatesDir+"feature.go."+format, lang)
			f.engine.RenderAndServePage(w, r, pageContent, breadcrumbs, key, lang)
		case engine.FormatJSON:
			featJSON, err := feat.MarshalJSON()
			if err != nil {
				http.Error(w, "Failed to marshal Feature to JSON", http.StatusInternalServerError)
				return
			}
			engine.SafeWrite(w.Write, featJSON)
		default:
			http.NotFound(w, r)
		}
	}
}

// featureCollectionPage enriched FeatureCollection for HTML representation.
type featureCollectionPage struct {
	*geojson.FeatureCollection

	CollectionID string
	Metadata     *engine.GeoSpatialCollectionMetadata
}

// featurePage enriched Feature for HTML representation.
type featurePage struct {
	*geojson.Feature

	FeatureID string
	Metadata  *engine.GeoSpatialCollectionMetadata
}

func (f *Features) cacheCollectionsMetadata() map[string]*engine.GeoSpatialCollectionMetadata {
	result := make(map[string]*engine.GeoSpatialCollectionMetadata)
	for _, collection := range f.engine.Config.OgcAPI.Features.Collections {
		result[collection.ID] = collection.Metadata
	}
	return result
}

func (f *Features) getCollectionTitle(collectionID string, collectionMetadata *engine.GeoSpatialCollectionMetadata) string {
	title := collectionID
	if collectionMetadata != nil && collectionMetadata.Title != nil {
		title = *collectionMetadata.Title
	}
	return title
}
