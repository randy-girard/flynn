package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
)

//go:embed data/*
var dataFS embed.FS

// statusResponse is returned by GET /status so the upgrade smoke can prove
// the slug (embedded blobs) and every attached datastore env survived a
// flynn-host update.
type statusResponse struct {
	OK        bool            `json:"ok"`
	Message   string          `json:"message"`
	BlobCount int             `json:"blob_count"`
	Resources map[string]bool `json:"resources"`
}

func blobCount(fsys embed.FS) int {
	n := 0
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, "blob-") || strings.HasSuffix(name, ".txt") {
			n++
		}
		return nil
	})
	return n
}

func resourceFlags(getenv func(string) string) map[string]bool {
	return map[string]bool{
		"postgres":   getenv("FLYNN_POSTGRES") != "",
		"mysql":      getenv("FLYNN_MYSQL") != "",
		"mongodb":    getenv("FLYNN_MONGO") != "",
		"redis":      getenv("FLYNN_REDIS") != "" || getenv("REDIS_URL") != "",
		"kafka":      getenv("FLYNN_KAFKA") != "",
		"clickhouse": getenv("FLYNN_CLICKHOUSE") != "",
	}
}

func buildStatus(fsys embed.FS, getenv func(string) string) statusResponse {
	res := resourceFlags(getenv)
	missing := make([]string, 0, len(res))
	for name, ok := range res {
		if !ok {
			missing = append(missing, name)
		}
	}
	msg := "ok"
	all := len(missing) == 0
	if !all {
		msg = "missing datastore env: " + strings.Join(missing, ",")
	}
	return statusResponse{
		OK:        all,
		Message:   msg,
		BlobCount: blobCount(fsys),
		Resources: res,
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		st := buildStatus(dataFS, os.Getenv)
		w.Header().Set("Content-Type", "application/json")
		if !st.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(st)
	})
	addr := ":" + os.Getenv("PORT")
	log.Fatal(http.ListenAndServe(addr, mux))
}
