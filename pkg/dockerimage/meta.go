package dockerimage

const (
	// MetaListenPort is stored on imported image artifacts so releases can
	// configure the app service port without re-parsing the docker config.
	MetaListenPort = "dockerimage.listen_port"
	// MetaEntrypoint is the JSON array of the image ENTRYPOINT. flynn.yml uses
	// it to replace CMD without dropping a non-shell entrypoint.
	MetaEntrypoint = "dockerimage.entrypoint"
	// MetaCmd is the JSON array of the image CMD.
	MetaCmd = "dockerimage.cmd"
)
