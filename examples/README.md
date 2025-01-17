# Examples

Checkout the examples below to see how GoKoala works.

## OGC API Tiles example

This example uses vector tiles from the [PDOK BGT dataset](https://www.pdok.nl/introductie/-/article/basisregistratie-grootschalige-topografie-bgt-) (a small subset, just for demo purposes). 

- Start GoKoala as specified in the root [README](../README.md#run) 
  and provide `config_vectortiles.yaml` as the config file.
- Open http://localhost:8080 to explore the landing page
- Call http://localhost:8080/tiles/NetherlandsRDNewQuad/12/2235/2031.pbf to download a specific tile

## OGC API Features example

There are 2 examples configurations:
- `config_features_local.yaml` - use the local [addresses.gpkg](resources%2Faddresses.gpkg) geopackage
- `config_features_azure.yaml` - use [addresses.gpkg](resources%2Faddresses.gpkg) hosted in Azure Blob as a [Cloud-Backed SQLite/Geopackage](https://sqlite.org/cloudsqlite/doc/trunk/www/index.wiki).

For the local version just start GoKoala as specified in the root [README](../README.md#run)
and provide the mentioned config file.

For the Azure example we use a local Azurite emulator which contains the cloud-backed `addresses.gpkg`:
- Run `docker-compose -f docker-compose-features-azure.yaml up`
- Open http://localhost:8080 to explore the landing page
- Call http://localhost:8080/collections/addresses/items and notice in the Azurite log that features are streamed from blob storage

## OGC API 3D GeoVolumes example

This example uses 3D tiles of New York.

- Start GoKoala as specified in the root [README](../README.md#run)
  and provide `config_3d.yaml` as the config file.
- Open http://localhost:8080 to explore the landing page
- Call http://localhost:8080/collections/NewYork/3dtiles/6/0/1.b3dm to download a specific 3D tile

## Multiple OGC APIs for a single collection example

This example demonstrates that you can have a collection (NewYork in this case) that offers
multiple OGC APIs: both OGC API 3D GeoVolumes and OGC API Features.

To keep the config DRY we use YAML anchors+aliases to reference common metadata for a collection.

- Start GoKoala as specified in the root [README](../README.md#run)
  and provide `config_multiple_ogc_apis_single_collection.yaml` as the config file.
- Open http://localhost:8080 to explore the landing page
- Call http://localhost:8080/collections/NewYork/ to view the collection

