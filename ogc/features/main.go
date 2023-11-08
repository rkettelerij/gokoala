package features

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"

	"github.com/PDOK/gokoala/engine"
	"github.com/PDOK/gokoala/ogc/common/geospatial"
	"github.com/PDOK/gokoala/ogc/features/datasources"
	"github.com/PDOK/gokoala/ogc/features/datasources/geopackage"
	"github.com/PDOK/gokoala/ogc/features/datasources/postgis"
	"github.com/PDOK/gokoala/ogc/features/domain"
	"github.com/go-chi/chi/v5"
	"github.com/go-spatial/geom"
)

const (
	templatesDir = "ogc/features/templates/"
)

var (
	collectionsMetadata map[string]*engine.GeoSpatialCollectionMetadata
)

type Features struct {
	engine     *engine.Engine
	datasource datasources.Datasource

	html *htmlFeatures
	json *jsonFeatures
}

func NewFeatures(e *engine.Engine, router *chi.Mux) *Features {
	cfg := e.Config.OgcAPI.Features

	var datasource datasources.Datasource
	if cfg.Datasource.GeoPackage != nil {
		datasource = geopackage.NewGeoPackage(cfg.Collections, *cfg.Datasource.GeoPackage)
	} else if cfg.Datasource.PostGIS != nil {
		datasource = postgis.NewPostGIS()
	}
	e.RegisterShutdownHook(datasource.Close)

	f := &Features{
		engine:     e,
		datasource: datasource,
		html:       newHTMLFeatures(e),
		json:       newJSONFeatures(e),
	}
	collectionsMetadata = f.cacheCollectionsMetadata()

	router.Get(geospatial.CollectionsPath+"/{collectionId}/items", f.CollectionContent())
	router.Get(geospatial.CollectionsPath+"/{collectionId}/items/{featureId}", f.Feature())
	return f
}

// CollectionContent serve a FeatureCollection with the given collectionId
func (f *Features) CollectionContent() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		collectionID, encodedCursor, limit, bbox, err := f.parseFeatureCollectionRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		url := featureCollectionURL{*f.engine.Config.BaseURL.URL, r.URL.Query()}
		if err = url.validateNoUnknownParams(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := collectionsMetadata[collectionID]; !ok {
			log.Printf("collection %s doesn't exist in this features service", collectionID)
			http.NotFound(w, r)
			return
		}

		fc, newCursor, err := f.datasource.GetFeatures(r.Context(), collectionID, datasources.FeatureOptions{
			Cursor:  encodedCursor.Decode(url.checksum()),
			Limit:   limit,
			Bbox:    bbox,
			BboxCrs: 28992, // TODO turn bbox-crs param (which contains an URI) to an EPSG code.
			// TODO set crs, etc
		})
		if err != nil {
			// log error, but sent generic message to client to prevent possible information leakage from datasource
			msg := fmt.Sprintf("failed to retrieve feature collection %s", collectionID)
			log.Printf("%s, error: %v\n", msg, err)
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		if fc == nil {
			log.Printf("no results found for collection '%s' with params: %s",
				collectionID, r.URL.Query().Encode())
			http.NotFound(w, r)
			return
		}

		switch f.engine.CN.NegotiateFormat(r) {
		case engine.FormatHTML:
			f.html.features(w, r, collectionID, newCursor, url, limit, fc)
		case engine.FormatJSON:
			f.json.featuresAsGeoJSON(w, collectionID, newCursor, url, fc)
		case engine.FormatJSONFG:
			f.json.featuresAsJSONFG()
		default:
			http.NotFound(w, r)
			return
		}
	}
}

// Feature serves a single Feature
func (f *Features) Feature() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		collectionID := chi.URLParam(r, "collectionId")
		featureID, err := strconv.Atoi(chi.URLParam(r, "featureId"))
		if err != nil {
			http.Error(w, "feature ID must be a number", http.StatusBadRequest)
			return
		}
		url := featureURL{*f.engine.Config.BaseURL.URL, r.URL.Query()}
		if err = url.validateNoUnknownParams(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		feat, err := f.datasource.GetFeature(r.Context(), collectionID, int64(featureID))
		if err != nil {
			// log error, but sent generic message to client to prevent possible information leakage from datasource
			msg := fmt.Sprintf("failed to retrieve feature %d in collection %s", featureID, collectionID)
			log.Printf("%s, error: %v\n", msg, err)
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		if feat == nil {
			log.Printf("no result found for collection '%s' and feature id: %d",
				collectionID, featureID)
			http.NotFound(w, r)
			return
		}

		switch f.engine.CN.NegotiateFormat(r) {
		case engine.FormatHTML:
			f.html.feature(w, r, collectionID, feat)
		case engine.FormatJSON:
			f.json.featureAsGeoJSON(w, collectionID, feat, url)
		case engine.FormatJSONFG:
			f.json.featureAsJSONFG()
		default:
			http.NotFound(w, r)
			return
		}
	}
}

func (f *Features) cacheCollectionsMetadata() map[string]*engine.GeoSpatialCollectionMetadata {
	result := make(map[string]*engine.GeoSpatialCollectionMetadata)
	for _, collection := range f.engine.Config.OgcAPI.Features.Collections {
		result[collection.ID] = collection.Metadata
	}
	return result
}

func (f *Features) parseFeatureCollectionRequest(r *http.Request) (string, domain.EncodedCursor, int, *geom.Extent, error) {
	collectionID := chi.URLParam(r, "collectionId")
	encodedCursor := domain.EncodedCursor(r.URL.Query().Get(cursorParam))
	limit, limitErr := f.parseLimit(r.URL.Query())
	bbox, bboxErr := f.parseBbox(r.URL.Query())
	dateTimeErr := f.parseDateTime(r.URL.Query())
	return collectionID, encodedCursor, limit, bbox, errors.Join(limitErr, bboxErr, dateTimeErr)
}

func (f *Features) parseLimit(params neturl.Values) (int, error) {
	limit := f.engine.Config.OgcAPI.Features.Limit.Default
	var err error
	if params.Get(limitParam) != "" {
		limit, err = strconv.Atoi(params.Get(limitParam))
		if err != nil {
			err = fmt.Errorf("limit must be numeric")
		}
		// OpenAPI validation already guards against exceeding max limit, this is just a defense in-depth measure.
		if limit > f.engine.Config.OgcAPI.Features.Limit.Max {
			limit = f.engine.Config.OgcAPI.Features.Limit.Max
		}
	}
	if limit < 0 {
		err = fmt.Errorf("limit can't be negative")
	}
	return limit, err
}

func (f *Features) parseBbox(params neturl.Values) (*geom.Extent, error) {
	if params.Get(bboxParam) == "" {
		return nil, nil //nolint:nilnil
	}
	bboxValues := strings.Split(params.Get(bboxParam), ",")
	if len(bboxValues) != 4 {
		return nil, fmt.Errorf("bbox should contain exactly 4 values " +
			"seperated by commas: minx,miny,maxx,maxy")
	}

	var err error
	var extent geom.Extent
	for i, v := range bboxValues {
		extent[i], err = strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse value %s in bbox, error: %w", v, err)
		}
	}
	return &extent, nil
}

func (f *Features) parseDateTime(params neturl.Values) error {
	if params.Get(dateTimeParam) != "" {
		return fmt.Errorf("datetime param is currently not supported")
	}
	return nil
}
