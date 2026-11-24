package utils

import (
	"fmt"
	"strings"
)

const baseURL = "http://blobstore.discoverd"

// ConfigURL is the config of a layer in the shared store (cluster-key
// callers that name no app).
func ConfigURL(id string) string {
	return fmt.Sprintf("%s/tarreceive/layers/%s.json", baseURL, id)
}

// LayerURLTemplate is the layer template of the shared store.
const LayerURLTemplate = baseURL + "/tarreceive/layers/{id}.squashfs"

// LayerURL is a layer in the shared store.
func LayerURL(id string) string {
	return fmt.Sprintf("%s/tarreceive/layers/%s.squashfs", baseURL, id)
}

// layerDir is where tarreceive keeps the layers of app (EDGE-35): under the
// app's own prefix, so they count toward its APP_QUOTA. An empty app is the
// shared store.
func layerDir(app string) string {
	if app == "" {
		return baseURL + "/tarreceive/layers/"
	}
	return baseURL + "/tarreceive/apps/" + app + "/layers/"
}

// LayerURLTemplateFor is the layer template of an artifact whose layers
// tarreceive stored for app.
func LayerURLTemplateFor(app string) string {
	return layerDir(app) + "{id}.squashfs"
}

// LayerURLFor is the squashfs of layer id stored for app.
func LayerURLFor(app, id string) string {
	return layerDir(app) + id + ".squashfs"
}

// ConfigURLFor is the config of layer id stored for app.
func ConfigURLFor(app, id string) string {
	return layerDir(app) + id + ".json"
}

// ConfigURLForLayerURL is the config stored next to a tarreceive layer
// squashfs URL.
func ConfigURLForLayerURL(layerURL string) string {
	return strings.TrimSuffix(layerURL, ".squashfs") + ".json"
}
