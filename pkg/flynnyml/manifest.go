// Package flynnyml parses the container-stack manifest. Provisioning stays in
// flynn-app.json. This file moves with the docker builder when that builder
// leaves core.
package flynnyml

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/pkg/resname"
	"gopkg.in/yaml.v2"
)

const (
	// MaxBytes is the largest flynn.yml accepted before the build.
	MaxBytes = 1 << 20
	// MaxImages is the most unique Docker builds one flynn.yml may request.
	// The builder runs BuildKit once per image inside one job and a 4G temp
	// disk, deleting each image tar after upload. Eight keeps a push inside
	// that job.
	MaxImages = 8
	// SetupIgnoredLine is printed when flynn-app.json owns provisioning.
	SetupIgnoredLine = "-----> flynn.yml setup ignored; flynn-app.json already describes this app"
	// StackRequired is the heroku-24 failure. The buildpack build does not start.
	StackRequired = "flynn.yml requires the container stack (flynn stack:set container)"
)

// Manifest is a parsed flynn.yml.
type Manifest struct {
	Setup   *Setup
	Build   Build
	Release *Release
	Run     []RunProc
}

// Setup is first-deploy provisioning. It is not Docker build configuration.
type Setup struct {
	Addons []Addon
	Config map[string]string
}

// Addon is one setup.addons entry. Plan and Type are as written. The Flynn
// type is ResolveAddonType.
type Addon struct {
	Plan    string
	Type    string
	As      string
	Runtime string
}

// Build is the Docker section.
type Build struct {
	Images []Image
	Config map[string]string
}

// Image is one build.docker key. Context is the directory that contains the
// Dockerfile. "." is the repository root.
type Image struct {
	Name       string
	Dockerfile string
	Target     string
	Context    string
	Filename   string
}

// Release is the one-shot command. Image is empty when the file omitted it.
type Release struct {
	Image   string
	Command []string
}

// RunProc is one run key. Shell is set for a scalar. Command is set for an
// object. Image may be empty until Resolve.
type RunProc struct {
	Name    string
	Shell   string
	Command []string
	Image   string
	Exec    bool
}

// PlannedBuild is one BuildKit invocation. Names are the build.docker keys
// that share it.
type PlannedBuild struct {
	Dockerfile string
	Target     string
	Context    string
	Filename   string
	Names      []string
}

// Key identifies a shared build.
func (p PlannedBuild) Key() string {
	return p.Dockerfile + "\x00" + p.Target
}

// HasSetup reports addon or config work.
func (m *Manifest) HasSetup() bool {
	return m != nil && m.Setup != nil && (len(m.Setup.Addons) > 0 || len(m.Setup.Config) > 0)
}

// Parse validates flynn.yml. Catalog checks happen later, before the build.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("flynn.yml is larger than %d bytes", MaxBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("flynn.yml is empty")
	}
	var root map[interface{}]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("flynn.yml: %w", err)
	}
	doc, err := stringKeyMap(root)
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	for k := range doc {
		switch k {
		case "setup", "build", "release", "run":
		default:
			return nil, fmt.Errorf("flynn.yml: unknown field %q", k)
		}
	}
	if raw, ok := doc["setup"]; ok && raw != nil {
		setup, err := parseSetup(raw)
		if err != nil {
			return nil, err
		}
		m.Setup = setup
	}
	rawBuild, ok := doc["build"]
	if !ok || rawBuild == nil {
		return nil, fmt.Errorf("flynn.yml: build.docker is required")
	}
	images, cfg, err := parseBuild(data, rawBuild)
	if err != nil {
		return nil, err
	}
	m.Build.Images = images
	m.Build.Config = cfg
	if raw, ok := doc["release"]; ok && raw != nil {
		rel, err := parseRelease(raw)
		if err != nil {
			return nil, err
		}
		m.Release = rel
	}
	if raw, ok := doc["run"]; ok && raw != nil {
		run, err := parseRun(data, raw)
		if err != nil {
			return nil, err
		}
		m.Run = run
	}
	if err := m.limitImages(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manifest) limitImages() error {
	seen := map[string]struct{}{}
	for _, img := range m.Build.Images {
		seen[img.Dockerfile+"\x00"+img.Target] = struct{}{}
	}
	if len(seen) > MaxImages {
		return fmt.Errorf("flynn.yml: at most %d images", MaxImages)
	}
	return nil
}

// Resolve fills default run images and checks release.image.
func (m *Manifest) Resolve() error {
	if m == nil {
		return fmt.Errorf("flynn.yml is empty")
	}
	byName := m.byName()
	for i := range m.Run {
		img, err := m.resolveRunImage(m.Run[i], byName)
		if err != nil {
			return err
		}
		m.Run[i].Image = img
	}
	if m.Release != nil {
		if _, err := m.ReleaseImage(); err != nil {
			return err
		}
	}
	return nil
}

// Processes is the run list, or one process per built image except release.
func (m *Manifest) Processes() ([]RunProc, error) {
	if err := m.Resolve(); err != nil {
		return nil, err
	}
	if len(m.Run) > 0 {
		out := make([]RunProc, len(m.Run))
		copy(out, m.Run)
		return out, nil
	}
	var out []RunProc
	for _, img := range m.Build.Images {
		if img.Name == "release" {
			continue
		}
		out = append(out, RunProc{Name: img.Name, Image: img.Name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("flynn.yml: no process to run")
	}
	return out, nil
}

// PlannedBuilds groups keys that share a dockerfile and target.
func (m *Manifest) PlannedBuilds() ([]PlannedBuild, error) {
	if m == nil || len(m.Build.Images) == 0 {
		return nil, fmt.Errorf("flynn.yml: build.docker is required")
	}
	var out []PlannedBuild
	index := map[string]int{}
	for _, img := range m.Build.Images {
		k := img.Dockerfile + "\x00" + img.Target
		if i, ok := index[k]; ok {
			out[i].Names = append(out[i].Names, img.Name)
			continue
		}
		index[k] = len(out)
		out = append(out, PlannedBuild{
			Dockerfile: img.Dockerfile,
			Target:     img.Target,
			Context:    img.Context,
			Filename:   img.Filename,
			Names:      []string{img.Name},
		})
	}
	return out, nil
}

// ReleaseImage is the build key for release.command. An empty string means
// the file has no release section.
func (m *Manifest) ReleaseImage() (string, error) {
	if m == nil || m.Release == nil {
		return "", nil
	}
	if len(m.Release.Command) == 0 {
		return "", fmt.Errorf("flynn.yml: release.command is required")
	}
	byName := m.byName()
	if m.Release.Image != "" {
		if _, ok := byName[m.Release.Image]; !ok {
			return "", fmt.Errorf("flynn.yml: release.image %q is not a build.docker key", m.Release.Image)
		}
		return m.Release.Image, nil
	}
	if _, ok := byName["web"]; ok {
		return "web", nil
	}
	if len(m.Build.Images) == 1 {
		return m.Build.Images[0].Name, nil
	}
	return "", fmt.Errorf("flynn.yml: release.image is required")
}

func (m *Manifest) byName() map[string]Image {
	out := make(map[string]Image, len(m.Build.Images))
	for _, img := range m.Build.Images {
		out[img.Name] = img
	}
	return out
}

func (m *Manifest) resolveRunImage(p RunProc, byName map[string]Image) (string, error) {
	if p.Image != "" {
		if _, ok := byName[p.Image]; !ok {
			return "", fmt.Errorf("flynn.yml: run.%s.image %q is not a build.docker key", p.Name, p.Image)
		}
		return p.Image, nil
	}
	if _, ok := byName[p.Name]; ok {
		return p.Name, nil
	}
	if len(m.Build.Images) == 1 && m.Build.Images[0].Name == "web" {
		return "web", nil
	}
	return "", fmt.Errorf("flynn.yml: run.%s needs an image", p.Name)
}

func parseSetup(raw interface{}) (*Setup, error) {
	doc, err := stringKeyMap(raw)
	if err != nil {
		return nil, fmt.Errorf("flynn.yml: setup must be a map")
	}
	for k := range doc {
		switch k {
		case "addons", "config":
		default:
			return nil, fmt.Errorf("flynn.yml: unknown field setup.%s", k)
		}
	}
	s := &Setup{}
	if raw, ok := doc["config"]; ok && raw != nil {
		cfg, err := parseStringMap(raw, "setup.config")
		if err != nil {
			return nil, err
		}
		s.Config = cfg
	}
	if raw, ok := doc["addons"]; ok && raw != nil {
		addons, err := parseAddons(raw)
		if err != nil {
			return nil, err
		}
		s.Addons = addons
	}
	return s, nil
}

func parseAddons(raw interface{}) ([]Addon, error) {
	seq, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("flynn.yml: setup.addons must be a list")
	}
	out := make([]Addon, 0, len(seq))
	for i, item := range seq {
		doc, err := stringKeyMap(item)
		if err != nil {
			return nil, fmt.Errorf("flynn.yml: setup.addons[%d] must be a map", i)
		}
		addon := Addon{}
		for k, v := range doc {
			switch k {
			case "plan":
				addon.Plan, err = asString(v, "setup.addons.plan")
			case "type":
				addon.Type, err = asString(v, "setup.addons.type")
			case "as":
				addon.As, err = asString(v, "setup.addons.as")
			case "options":
				addon.Runtime, err = parseRuntime(v)
			default:
				return nil, fmt.Errorf("flynn.yml: unknown field setup.addons.%s", k)
			}
			if err != nil {
				return nil, err
			}
		}
		if strings.TrimSpace(addon.Plan) == "" && strings.TrimSpace(addon.Type) == "" {
			return nil, fmt.Errorf("flynn.yml: setup.addons[%d] needs a plan or type", i)
		}
		out = append(out, addon)
	}
	return out, nil
}

func parseRuntime(raw interface{}) (string, error) {
	doc, err := stringKeyMap(raw)
	if err != nil {
		return "", fmt.Errorf("flynn.yml: addon options must be a map")
	}
	for k := range doc {
		if k != "runtime" {
			return "", fmt.Errorf("flynn.yml: unknown addon option %q", k)
		}
	}
	if raw, ok := doc["runtime"]; ok {
		return asString(raw, "options.runtime")
	}
	return "", nil
}

func parseBuild(src []byte, raw interface{}) ([]Image, map[string]string, error) {
	doc, err := stringKeyMap(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("flynn.yml: build must be a map")
	}
	for k := range doc {
		switch k {
		case "docker", "config":
		default:
			return nil, nil, fmt.Errorf("flynn.yml: unknown field build.%s", k)
		}
	}
	rawDocker, ok := doc["docker"]
	if !ok || rawDocker == nil {
		return nil, nil, fmt.Errorf("flynn.yml: build.docker is required")
	}
	docker, err := stringKeyMap(rawDocker)
	if err != nil || len(docker) == 0 {
		return nil, nil, fmt.Errorf("flynn.yml: build.docker is required")
	}
	names := orderedKeys(src, docker, "build", "docker")
	images := make([]Image, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		if !validName(name) {
			return nil, nil, fmt.Errorf("flynn.yml: invalid image name %q", name)
		}
		if _, ok := seen[name]; ok {
			return nil, nil, fmt.Errorf("flynn.yml: duplicate image %q", name)
		}
		seen[name] = struct{}{}
		img, err := parseImage(name, docker[name])
		if err != nil {
			return nil, nil, err
		}
		images = append(images, img)
	}
	var cfg map[string]string
	if raw, ok := doc["config"]; ok && raw != nil {
		cfg, err = parseStringMap(raw, "build.config")
		if err != nil {
			return nil, nil, err
		}
	}
	return images, cfg, nil
}

func parseImage(name string, raw interface{}) (Image, error) {
	if s, ok := raw.(string); ok {
		return imageFromPath(name, s, "")
	}
	doc, err := stringKeyMap(raw)
	if err != nil {
		return Image{}, fmt.Errorf("flynn.yml: build.docker.%s must be a path or a map", name)
	}
	for k := range doc {
		switch k {
		case "dockerfile", "target":
		default:
			return Image{}, fmt.Errorf("flynn.yml: unknown field build.docker.%s.%s", name, k)
		}
	}
	rawPath, ok := doc["dockerfile"]
	if !ok {
		return Image{}, fmt.Errorf("flynn.yml: build.docker.%s.dockerfile is required", name)
	}
	p, err := asString(rawPath, "build.docker."+name+".dockerfile")
	if err != nil {
		return Image{}, err
	}
	target := ""
	if raw, ok := doc["target"]; ok {
		target, err = asString(raw, "build.docker."+name+".target")
		if err != nil {
			return Image{}, err
		}
	}
	return imageFromPath(name, p, target)
}

func imageFromPath(name, p, target string) (Image, error) {
	cleaned, err := cleanRepoPath(p)
	if err != nil {
		return Image{}, err
	}
	dir := path.Dir(cleaned)
	if dir == "" {
		dir = "."
	}
	return Image{
		Name:       name,
		Dockerfile: cleaned,
		Target:     target,
		Context:    dir,
		Filename:   path.Base(cleaned),
	}, nil
}

func parseRelease(raw interface{}) (*Release, error) {
	doc, err := stringKeyMap(raw)
	if err != nil {
		return nil, fmt.Errorf("flynn.yml: release must be a map")
	}
	rel := &Release{}
	for k, v := range doc {
		switch k {
		case "image":
			rel.Image, err = asString(v, "release.image")
		case "command":
			rel.Command, err = parseArgv(v, "release.command")
		default:
			return nil, fmt.Errorf("flynn.yml: unknown field release.%s", k)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(rel.Command) == 0 {
		return nil, fmt.Errorf("flynn.yml: release.command is required")
	}
	return rel, nil
}

func parseRun(src []byte, raw interface{}) ([]RunProc, error) {
	doc, err := stringKeyMap(raw)
	if err != nil {
		return nil, fmt.Errorf("flynn.yml: run must be a map")
	}
	if len(doc) == 0 {
		return nil, nil
	}
	names := orderedKeys(src, doc, "run")
	out := make([]RunProc, 0, len(names))
	for _, name := range names {
		if !validName(name) {
			return nil, fmt.Errorf("flynn.yml: invalid process name %q", name)
		}
		proc, err := parseRunProc(name, doc[name])
		if err != nil {
			return nil, err
		}
		out = append(out, proc)
	}
	return out, nil
}

func parseRunProc(name string, raw interface{}) (RunProc, error) {
	if s, ok := raw.(string); ok {
		if strings.TrimSpace(s) == "" {
			return RunProc{}, fmt.Errorf("flynn.yml: run.%s is empty", name)
		}
		if strings.ContainsAny(s, "\r\n") {
			return RunProc{}, fmt.Errorf("flynn.yml: run.%s must be a single line", name)
		}
		return RunProc{Name: name, Shell: s}, nil
	}
	doc, err := stringKeyMap(raw)
	if err != nil {
		return RunProc{}, fmt.Errorf("flynn.yml: run.%s must be a string or a map", name)
	}
	proc := RunProc{Name: name, Exec: true}
	for k, v := range doc {
		switch k {
		case "command":
			proc.Command, err = parseArgv(v, "run."+name+".command")
		case "image":
			proc.Image, err = asString(v, "run."+name+".image")
		default:
			return RunProc{}, fmt.Errorf("flynn.yml: unknown field run.%s.%s", name, k)
		}
		if err != nil {
			return RunProc{}, err
		}
	}
	if len(proc.Command) == 0 {
		return RunProc{}, fmt.Errorf("flynn.yml: run.%s.command is required", name)
	}
	return proc, nil
}

func parseArgv(raw interface{}, what string) ([]string, error) {
	seq, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("flynn.yml: %s must be a list of strings", what)
	}
	if len(seq) == 0 {
		return nil, fmt.Errorf("flynn.yml: %s is empty", what)
	}
	out := make([]string, len(seq))
	for i, item := range seq {
		s, err := asString(item, what)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("flynn.yml: %s has an empty argument", what)
		}
		out[i] = s
	}
	return out, nil
}

func parseStringMap(raw interface{}, what string) (map[string]string, error) {
	doc, err := stringKeyMap(raw)
	if err != nil {
		return nil, fmt.Errorf("flynn.yml: %s must be a map of strings", what)
	}
	out := make(map[string]string, len(doc))
	for k, v := range doc {
		s, err := asString(v, what+"."+k)
		if err != nil {
			return nil, err
		}
		out[k] = s
	}
	return out, nil
}

func asString(v interface{}, what string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("flynn.yml: %s must be a string", what)
	}
	if strings.ContainsAny(s, "\r\n") {
		return "", fmt.Errorf("flynn.yml: %s must be a single line", what)
	}
	return s, nil
}

func stringKeyMap(v interface{}) (map[string]interface{}, error) {
	switch t := v.(type) {
	case map[string]interface{}:
		return t, nil
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			s, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("flynn.yml: invalid key %v", k)
			}
			out[s] = val
		}
		return out, nil
	default:
		return nil, fmt.Errorf("flynn.yml: expected a map")
	}
}

func orderedKeys(src []byte, doc map[string]interface{}, path ...string) []string {
	keys, ok := blockKeys(src, path...)
	if ok && sameKeys(keys, doc) {
		return keys
	}
	keys = keys[:0]
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sameKeys(keys []string, doc map[string]interface{}) bool {
	if len(keys) != len(doc) {
		return false
	}
	seen := map[string]struct{}{}
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			return false
		}
		seen[k] = struct{}{}
		if _, ok := doc[k]; !ok {
			return false
		}
	}
	return true
}

type frame struct {
	indent int
	key    string
}

// blockKeys returns the child keys of path in source order. Block style only.
func blockKeys(src []byte, path ...string) ([]string, bool) {
	var stack []frame
	var keys []string
	found := false
	for _, line := range bytes.Split(src, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		indent, rest := lineIndent(string(line))
		rest = strings.TrimSpace(rest)
		if rest == "" || strings.HasPrefix(rest, "-") {
			continue
		}
		colon := strings.Index(rest, ":")
		if colon <= 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(rest[:colon]), `"'`)
		if key == "" {
			continue
		}
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{indent: indent, key: key})
		if len(stack) != len(path)+1 {
			continue
		}
		match := true
		for i, p := range path {
			if stack[i].key != p {
				match = false
				break
			}
		}
		if match {
			found = true
			keys = append(keys, key)
		}
	}
	return keys, found
}

func lineIndent(line string) (int, string) {
	n := 0
	i := 0
	for i < len(line) {
		switch line[i] {
		case ' ':
			n++
			i++
		case '\t':
			n += 2
			i++
		default:
			return n, line[i:]
		}
	}
	return n, ""
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

func cleanRepoPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return "", fmt.Errorf("flynn.yml: dockerfile path is empty")
	}
	if strings.ContainsRune(p, 0) || strings.Contains(p, "\\") || strings.Contains(p, "\n") {
		return "", fmt.Errorf("flynn.yml: dockerfile path %q is invalid", p)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("flynn.yml: dockerfile path %q must stay inside the repository", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("flynn.yml: dockerfile path %q must stay inside the repository", p)
		}
	}
	return path.Clean(p), nil
}

var planPrefixes = []struct {
	prefix string
	typ    string
}{
	{"heroku-postgresql", "postgres"},
	{"heroku-redis", "redis"},
	{"openredis", "redis"},
	{"jawsdb-maria", "mysql"},
	{"jawsdb", "mysql"},
	{"cleardb", "mysql"},
	{"mongolab", "mongodb"},
	{"mongodb", "mongodb"},
}

// ResolveAddonType maps a Heroku plan prefix or checks an installed type.
// known reports names from flynn addons:types. The tier after a colon is ignored.
func ResolveAddonType(plan, typ string, known func(string) bool) (string, error) {
	plan = strings.TrimSpace(plan)
	typ = strings.TrimSpace(typ)
	if plan == "" && typ == "" {
		return "", fmt.Errorf("flynn.yml: addon needs a plan or type")
	}
	if known == nil {
		known = func(string) bool { return false }
	}
	var fromPlan string
	if plan != "" {
		mapped, ok := mapPlan(plan)
		if ok {
			fromPlan = mapped
		} else {
			name := plan
			if i := strings.IndexByte(name, ':'); i >= 0 {
				name = name[:i]
			}
			name = strings.ToLower(strings.TrimSpace(name))
			if !known(name) {
				return "", fmt.Errorf("flynn.yml: unknown addon %q", plan)
			}
			fromPlan = name
		}
	}
	if typ != "" {
		t := strings.ToLower(typ)
		if !known(t) {
			return "", fmt.Errorf("flynn.yml: unknown addon type %q", typ)
		}
		if fromPlan != "" && fromPlan != t {
			return "", fmt.Errorf("flynn.yml: addon plan %q is %s, not %s", plan, fromPlan, t)
		}
		return t, nil
	}
	return fromPlan, nil
}

func mapPlan(plan string) (string, bool) {
	plan = strings.ToLower(strings.TrimSpace(plan))
	for _, p := range planPrefixes {
		if plan == p.prefix || strings.HasPrefix(plan, p.prefix+":") {
			return p.typ, true
		}
	}
	return "", false
}

// ConfigCollides rejects setup.config keys the addons will inject.
func ConfigCollides(config map[string]string, types, as []string) error {
	bad := map[string]struct{}{}
	for _, typ := range types {
		if k := conventionalURL(typ); k != "" {
			bad[k] = struct{}{}
		}
	}
	for _, name := range as {
		if k := resname.AsURLKey(name); k != "" {
			bad[k] = struct{}{}
		}
	}
	for k := range config {
		if _, ok := bad[k]; ok {
			return fmt.Errorf("flynn.yml: setup.config %s is set by an addon", k)
		}
	}
	return nil
}

func conventionalURL(typ string) string {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "postgres", "mysql", "mongodb":
		return "DATABASE_URL"
	case "redis":
		return "REDIS_URL"
	case "kafka":
		return "KAFKA_URL"
	case "clickhouse":
		return "CLICKHOUSE_URL"
	default:
		return ""
	}
}
