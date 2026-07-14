package dockerimage

// Manifest is used to read manifest.json from the output of 'docker save'.
type Manifest struct {
	Config string   `json:"Config"`
	Layers []string `json:"Layers"`
}

// Config is used to read image config from the output of 'docker save'.
type Config struct {
	Config struct {
		Env          []string
		Cmd          []string
		WorkingDir   string
		Entrypoint   []string
		ExposedPorts map[string]interface{} `json:"ExposedPorts"`
	} `json:"config"`
	Rootfs struct {
		Diffs []string `json:"diff_ids"`
	} `json:"rootfs"`
}
