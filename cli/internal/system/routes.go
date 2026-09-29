package system

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Routes serves backups over the native API.
func (b *Backup) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/system/backup", "homecloud:CreateBackup", b.download)
}

func (b *Backup) download(c *httpx.Ctx) (any, error) {
	volumes := c.Query("volumes") != "false"
	name := fmt.Sprintf("homecloud-backup-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
	c.W.Header().Set("Content-Type", "application/gzip")
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	c.W.WriteHeader(http.StatusOK)
	c.MarkWritten()
	if err := b.Write(c.R.Context(), c.W, volumes, log.Printf); err != nil {
		// Headers are sent; the truncated gzip stream tells the client it failed.
		log.Printf("backup failed: %v", err)
		panic(http.ErrAbortHandler)
	}
	return nil, nil
}
