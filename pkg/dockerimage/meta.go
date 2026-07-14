package dockerimage

const (
	// MetaListenPort is stored on imported image artifacts so releases can
	// configure the app service port without re-parsing the docker config.
	MetaListenPort = "dockerimage.listen_port"
)
