package imageenv

// IDs holds controller artifact IDs for Flynn builder images.
type IDs struct {
	Redis         string
	SlugBuilder   string
	SlugRunner    string
	DockerBuilder string
	Kafka         string
	ClickHouse    string
}

// Update sets builder image ID env vars on a release. Missing keys are added
// so clusters bootstrapped before container stack support are migrated.
// Returns true if env was changed.
func Update(env map[string]string, ids IDs) bool {
	if env == nil {
		return false
	}
	updated := false
	for envKey, newID := range map[string]string{
		"REDIS_IMAGE_ID":            ids.Redis,
		"SLUGBUILDER_IMAGE_ID":      ids.SlugBuilder,
		"SLUGBUILDER_24_IMAGE_ID":   ids.SlugBuilder,
		"SLUGRUNNER_IMAGE_ID":       ids.SlugRunner,
		"SLUGRUNNER_24_IMAGE_ID":    ids.SlugRunner,
		"DOCKERBUILDER_24_IMAGE_ID": ids.DockerBuilder,
		"KAFKA_IMAGE_ID":            ids.Kafka,
		"CLICKHOUSE_IMAGE_ID":       ids.ClickHouse,
	} {
		if newID == "" {
			continue
		}
		if id, ok := env[envKey]; !ok || id != newID {
			env[envKey] = newID
			updated = true
		}
	}
	for _, prefix := range []string{"REDIS", "SLUGBUILDER", "SLUGRUNNER"} {
		uriKey := prefix + "_IMAGE_URI"
		if _, ok := env[uriKey]; !ok {
			continue
		}
		delete(env, uriKey)
		idKey := prefix + "_IMAGE_ID"
		newID := map[string]string{
			"REDIS":       ids.Redis,
			"SLUGBUILDER": ids.SlugBuilder,
			"SLUGRUNNER":  ids.SlugRunner,
		}[prefix]
		if newID != "" {
			env[idKey] = newID
			updated = true
		}
	}
	return updated
}
