package geopackage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/PDOK/gokoala/engine"
	"github.com/PDOK/gokoala/engine/util"
	"github.com/PDOK/gokoala/ogc/features/datasources"
	"github.com/PDOK/gokoala/ogc/features/domain"
	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/gpkg"
	"github.com/go-spatial/geom/encoding/wkt"
	"github.com/jmoiron/sqlx"
	"github.com/mattn/go-sqlite3"
	"github.com/qustavo/sqlhooks/v2"

	_ "github.com/mattn/go-sqlite3" // import for side effect (= sqlite3 driver) only
)

const (
	sqliteDriverName = "sqlite3_with_extensions"
	bboxSizeBig      = 10000
)

// Load sqlite extensions once.
//
// Extensions are by default expected in /usr/lib. For spatialite you can
// alternatively/optionally set SPATIALITE_LIBRARY_PATH.
func init() {
	driver := &sqlite3.SQLiteDriver{
		Extensions: []string{
			path.Join(os.Getenv("SPATIALITE_LIBRARY_PATH"), "mod_spatialite"),
		},
	}
	sql.Register(sqliteDriverName, sqlhooks.Wrap(driver, &datasources.SQLLog{}))
}

type geoPackageBackend interface {
	getDB() *sqlx.DB
	close()
}

type featureTable struct {
	TableName          string    `db:"table_name"`
	DataType           string    `db:"data_type"`
	Identifier         string    `db:"identifier"`
	Description        string    `db:"description"`
	GeometryColumnName string    `db:"column_name"`
	GeometryType       string    `db:"geometry_type_name"`
	LastChange         time.Time `db:"last_change"`
	MinX               float64   `db:"min_x"` // bbox
	MinY               float64   `db:"min_y"` // bbox
	MaxX               float64   `db:"max_x"` // bbox
	MaxY               float64   `db:"max_y"` // bbox
	SRS                int64     `db:"srs_id"`
}

type GeoPackage struct {
	backend geoPackageBackend

	fidColumn                  string
	featureTableByCollectionID map[string]*featureTable
	queryTimeout               time.Duration
}

func NewGeoPackage(collections engine.GeoSpatialCollections, gpkgConfig engine.GeoPackage) *GeoPackage {
	g := &GeoPackage{}
	switch {
	case gpkgConfig.Local != nil:
		g.backend = newLocalGeoPackage(gpkgConfig.Local)
		g.fidColumn = gpkgConfig.Local.Fid
		g.queryTimeout = gpkgConfig.Local.GetQueryTimeout()
	case gpkgConfig.Cloud != nil:
		g.backend = newCloudBackedGeoPackage(gpkgConfig.Cloud)
		g.fidColumn = gpkgConfig.Cloud.Fid
		g.queryTimeout = gpkgConfig.Cloud.GetQueryTimeout()
	default:
		log.Fatal("unknown geopackage config encountered")
	}

	metadata, err := readDriverMetadata(g.backend.getDB())
	if err != nil {
		log.Fatalf("failed to connect with geopackage: %v", err)
	}
	log.Println(metadata)

	featureTables, err := readGpkgContents(collections, g.backend.getDB())
	if err != nil {
		log.Fatal(err)
	}
	g.featureTableByCollectionID = featureTables

	// assert that an index named <table>_spatial_idx exists on each feature table with the given columns
	g.assertIndexExistOnFeatureTables("_spatial_idx",
		strings.Join([]string{g.fidColumn, "minx", "maxx", "miny", "maxy"}, ","))

	return g
}

func (g *GeoPackage) Close() {
	g.backend.close()
}

func (g *GeoPackage) GetFeatures(ctx context.Context, collection string, options datasources.FeatureOptions) (*domain.FeatureCollection, domain.Cursors, error) {
	table, ok := g.featureTableByCollectionID[collection]
	if !ok {
		return nil, domain.Cursors{}, fmt.Errorf("can't query collection '%s' since it doesn't exist in "+
			"geopackage, available in geopackage: %v", collection, util.Keys(g.featureTableByCollectionID))
	}

	queryCtx, cancel := context.WithTimeout(ctx, g.queryTimeout) // https://go.dev/doc/database/cancel-operations
	defer cancel()

	query, queryArgs, err := g.makeFeaturesQuery(table, options)
	if err != nil {
		return nil, domain.Cursors{}, fmt.Errorf("failed to make features query, error: %w", err)
	}

	stmt, err := g.backend.getDB().PrepareNamedContext(queryCtx, query)
	if err != nil {
		return nil, domain.Cursors{}, fmt.Errorf("failed to prepare query '%s' error: %w", query, err)
	}
	defer stmt.Close()

	rows, err := stmt.QueryxContext(queryCtx, queryArgs)
	if err != nil {
		return nil, domain.Cursors{}, fmt.Errorf("failed to execute query '%s' error: %w", query, err)
	}
	defer rows.Close()

	var nextPrev *domain.PrevNextFID
	result := domain.FeatureCollection{}
	result.Features, nextPrev, err = domain.MapRowsToFeatures(rows, g.fidColumn, table.GeometryColumnName, readGpkgGeometry)
	if err != nil {
		return nil, domain.Cursors{}, err
	}
	if nextPrev == nil {
		return nil, domain.Cursors{}, nil
	}

	result.NumberReturned = len(result.Features)
	return &result, domain.NewCursors(*nextPrev, options.Cursor.FiltersChecksum), nil
}

func (g *GeoPackage) GetFeature(ctx context.Context, collection string, featureID int64) (*domain.Feature, error) {
	table, ok := g.featureTableByCollectionID[collection]
	if !ok {
		return nil, fmt.Errorf("can't query collection '%s' since it doesn't exist in "+
			"geopackage, available in geopackage: %v", collection, util.Keys(g.featureTableByCollectionID))
	}

	queryCtx, cancel := context.WithTimeout(ctx, g.queryTimeout) // https://go.dev/doc/database/cancel-operations
	defer cancel()

	query := fmt.Sprintf("select * from %s f where f.%s = :fid limit 1", table.TableName, g.fidColumn)
	stmt, err := g.backend.getDB().PrepareNamedContext(queryCtx, query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	rows, err := stmt.QueryxContext(queryCtx, map[string]any{"fid": featureID})
	if err != nil {
		return nil, fmt.Errorf("query '%s' failed: %w", query, err)
	}
	defer rows.Close()

	features, _, err := domain.MapRowsToFeatures(rows, g.fidColumn, table.GeometryColumnName, readGpkgGeometry)
	if err != nil {
		return nil, err
	}
	if len(features) != 1 {
		return nil, nil //nolint:nilnil
	}
	return features[0], nil
}

// Build specific features queries based on the given options.
// Make sure to use SQL bind variables and return named params: https://jmoiron.github.io/sqlx/#namedParams
func (g *GeoPackage) makeFeaturesQuery(table *featureTable, opt datasources.FeatureOptions) (string, map[string]any, error) {
	if opt.Bbox != nil {
		return g.makeBboxQuery(table, opt)
	}
	return g.makeDefaultQuery(table, opt)
}

func (g *GeoPackage) makeDefaultQuery(table *featureTable, opt datasources.FeatureOptions) (string, map[string]any, error) {
	defaultQuery := fmt.Sprintf(`
with 
    next as (select * from %[1]s where %[2]s >= :fid order by %[2]s asc limit :limit + 1),
    prev as (select * from %[1]s where %[2]s < :fid order by %[2]s desc limit :limit),
    nextprev as (select * from next union all select * from prev),
    nextprevfeat as (select *, lag(%[2]s, :limit) over (order by %[2]s) as prevfid, lead(%[2]s, :limit) over (order by %[2]s) as nextfid from nextprev)
select * from nextprevfeat where %[2]s >= :fid limit :limit
`, table.TableName, g.fidColumn)

	return defaultQuery, map[string]any{
		"fid":   opt.Cursor.FID,
		"limit": opt.Limit,
	}, nil
}

func (g *GeoPackage) makeBboxQuery(table *featureTable, opt datasources.FeatureOptions) (string, map[string]any, error) {
	bboxQuery := fmt.Sprintf(`
with 
     given_bbox as (select geomfromtext(:bboxWkt, :bboxCrs)),
     bbox_size as (select iif(count(id) < %[3]d, 'small', 'big') as bbox_size
                     from (select id from rtree_%[1]s_%[4]s
                           where minx <= :maxx and maxx >= :minx and miny <= :maxy and maxy >= :miny
                           limit %[3]d)),
     next_bbox_rtree as (select f.*
                         from %[1]s f inner join rtree_%[1]s_%[4]s rf on f.%[2]s = rf.id
                         where rf.minx <= :maxx and rf.maxx >= :minx and rf.miny <= :maxy and rf.maxy >= :miny
                           and st_intersects((select * from given_bbox), castautomagic(f.%[4]s)) = 1
                           and f.%[2]s >= :fid 
                         order by f.%[2]s asc 
                         limit (select iif(bbox_size == 'small', :limit + 1, 0) from bbox_size)),
     next_bbox_btree as (select f.*
                         from %[1]s f indexed by %[1]s_spatial_idx
                         where f.minx <= :maxx and f.maxx >= :minx and f.miny <= :maxy and f.maxy >= :miny
                           and st_intersects((select * from given_bbox), castautomagic(f.%[4]s)) = 1
                           and f.%[2]s >= :fid 
                         order by f.%[2]s asc 
                         limit (select iif(bbox_size == 'big', :limit + 1, 0) from bbox_size)),
     next as (select * from next_bbox_rtree union all select * from next_bbox_btree),
     prev_bbox_rtree as (select f.*
                         from %[1]s f inner join rtree_%[1]s_%[4]s rf on f.%[2]s = rf.id
                         where rf.minx <= :maxx and rf.maxx >= :minx and rf.miny <= :maxy and rf.maxy >= :miny
                           and st_intersects((select * from given_bbox), castautomagic(f.%[4]s)) = 1
                           and f.%[2]s < :fid 
                         order by f.%[2]s desc 
                         limit (select iif(bbox_size == 'small', :limit, 0) from bbox_size)),
     prev_bbox_btree as (select f.*
                         from %[1]s f indexed by %[1]s_spatial_idx
                         where f.minx <= :maxx and f.maxx >= :minx and f.miny <= :maxy and f.maxy >= :miny
                           and st_intersects((select * from given_bbox), castautomagic(f.%[4]s)) = 1
                           and f.%[2]s < :fid 
                         order by f.%[2]s desc 
                         limit (select iif(bbox_size == 'big', :limit, 0) from bbox_size)),
     prev as (select * from prev_bbox_rtree union all select * from prev_bbox_btree),
     nextprev as (select * from next union all select * from prev),
     nextprevfeat as (select *, lag(%[2]s, :limit) over (order by %[2]s) as prevfid, lead(%[2]s, :limit) over (order by %[2]s) as nextfid from nextprev)
select * from nextprevfeat where %[2]s >= :fid limit :limit
`, table.TableName, g.fidColumn, bboxSizeBig, table.GeometryColumnName)

	bboxAsWKT, err := wkt.EncodeString(opt.Bbox)
	if err != nil {
		return "", nil, err
	}
	return bboxQuery, map[string]any{
		"fid":     opt.Cursor.FID,
		"limit":   opt.Limit,
		"bboxWkt": bboxAsWKT,
		"maxx":    opt.Bbox.MaxX(),
		"minx":    opt.Bbox.MinX(),
		"maxy":    opt.Bbox.MaxY(),
		"miny":    opt.Bbox.MinY(),
		"bboxCrs": opt.BboxCrs}, nil
}

// Read metadata about gpkg and sqlite driver
func readDriverMetadata(db *sqlx.DB) (string, error) {
	type pragma struct {
		UserVersion string `db:"user_version"`
	}
	type metadata struct {
		Sqlite     string `db:"sqlite"`
		Spatialite string `db:"spatialite"`
		Arch       string `db:"arch"`
	}

	var m metadata
	err := db.QueryRowx(`
select sqlite_version() as sqlite, 
spatialite_version() as spatialite,  
spatialite_target_cpu() as arch`).StructScan(&m)
	if err != nil {
		return "", err
	}

	var gpkgVersion pragma
	_ = db.QueryRowx(`pragma user_version`).StructScan(&gpkgVersion)
	if gpkgVersion.UserVersion == "" {
		gpkgVersion.UserVersion = "unknown"
	}

	return fmt.Sprintf("geopackage version: %s, sqlite version: %s, spatialite version: %s on %s",
		gpkgVersion.UserVersion, m.Sqlite, m.Spatialite, m.Arch), nil
}

// Assert that an index on each feature table exists with the given suffix and covering the given columns, in the given order.
func (g *GeoPackage) assertIndexExistOnFeatureTables(expectedIndexNameSuffix string, expectedIndexColumns string) {
	for _, collection := range g.featureTableByCollectionID {
		expectedIndexName := collection.TableName + expectedIndexNameSuffix
		var actualIndexColumns string

		query := fmt.Sprintf(`
select group_concat(name) 
from pragma_index_info('%s') 
order by name asc`, expectedIndexName)

		err := g.backend.getDB().QueryRowx(query).Scan(&actualIndexColumns)
		if err != nil {
			log.Fatalf("missing index: failed to read index '%s' from table '%s'",
				expectedIndexName, collection.TableName)
		}
		if expectedIndexColumns != actualIndexColumns {
			log.Fatalf("incorrect index: expected index '%s' with columns '%s' to exist on table '%s', found indexed columns '%s'",
				expectedIndexName, expectedIndexColumns, collection.TableName, actualIndexColumns)
		}
	}
}

// Read gpkg_contents table. This table contains metadata about feature tables. The result is a mapping from
// collection ID -> feature table metadata. We match each feature table to the collection ID by looking at the
// 'identifier' column. Also in case there's no exact match between 'collection ID' and 'identifier' we use
// the explicitly configured 'datasource ID'
func readGpkgContents(collections engine.GeoSpatialCollections, db *sqlx.DB) (map[string]*featureTable, error) {
	query := `
select
	c.table_name, c.data_type, c.identifier, c.description, c.last_change,
	c.min_x, c.min_y, c.max_x, c.max_y, c.srs_id, gc.column_name, gc.geometry_type_name
from
	gpkg_contents c join gpkg_geometry_columns gc on c.table_name == gc.table_name
where
	c.data_type = 'features' and 
	c.min_x is not null`

	rows, err := db.Queryx(query)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve gpkg_contents using query: %v\n, error: %w", query, err)
	}
	defer rows.Close()

	result := make(map[string]*featureTable, 10)
	for rows.Next() {
		row := featureTable{}
		if err = rows.StructScan(&row); err != nil {
			return nil, fmt.Errorf("failed to read gpkg_contents record, error: %w", err)
		}
		if row.TableName == "" {
			return nil, fmt.Errorf("feature table name is blank, error: %w", err)
		}

		if len(collections) == 0 {
			result[row.Identifier] = &row
		} else {
			for _, collection := range collections {
				if row.Identifier == collection.ID {
					result[collection.ID] = &row
					break
				} else if hasMatchingDatasourceID(collection, row) {
					result[collection.ID] = &row
					break
				}
			}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no records found in gpkg_contents, can't serve features")
	}

	return result, nil
}

func hasMatchingDatasourceID(collection engine.GeoSpatialCollection, row featureTable) bool {
	return collection.Features != nil && collection.Features.DatasourceID != nil &&
		row.Identifier == *collection.Features.DatasourceID
}

func readGpkgGeometry(rawGeom []byte) (geom.Geometry, error) {
	geometry, err := gpkg.DecodeGeometry(rawGeom)
	if err != nil {
		return nil, err
	}
	return geometry.Geometry, nil
}
