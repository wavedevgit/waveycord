package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// handleCreateUploads stages attachments and returns the documented upload URLs.
func (s *Server) handleCreateUploads(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.AttachFiles|perm.SendMessages)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Files []struct {
			ID          *int    `json:"id"`
			Filename    string  `json:"filename"`
			Description *string `json:"description"`
			FileSize    int64   `json:"file_size"`
		} `json:"files"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len(req.Files) == 0 {
		c.FailForm("files", "This field is required")
		return
	}
	if len(req.Files) > 10 {
		c.FailForm("files", "Must be 10 or fewer in length")
		return
	}
	type uploadView struct {
		ID             string  `json:"id"`
		Filename       string  `json:"filename"`
		Description    *string `json:"description"`
		UploadFilename string  `json:"upload_filename"`
		UploadURL      string  `json:"upload_url"`
	}
	out := make([]uploadView, 0, len(req.Files))
	for _, f := range req.Files {
		if strings.TrimSpace(f.Filename) == "" {
			c.FailForm("files", "filename is required")
			return
		}
		if f.FileSize > s.cfg.MaxUploadBytes {
			c.JSONStatus(413, map[string]any{"code": 40005, "message": "Request entity too large"})
			return
		}
		u, err := s.Store.CreateUpload(ch.ID, c.User.ID, f.Filename, nil, f.FileSize)
		if err != nil {
			fail(c, err)
			return
		}
		out = append(out, uploadView{
			ID: u.ID, Filename: u.Filename, Description: f.Description,
			UploadFilename: u.ID + "/" + f.Filename,
			UploadURL:      s.cfg.CDNBase + "/api/v10/attachments/" + u.ID + "/" + urlEscape(f.Filename),
		})
	}
	c.JSON(out)
}

// handleUploadBody accepts the raw bytes for a staged attachment.
func (s *Server) handleUploadBody(c *Ctx) {
	idv := c.Param("uploadID")
	if _, err := s.Store.Upload(idv); err != nil {
		c.JSONStatus(404, map[string]any{"code": 10005, "message": "Unknown Attachment"})
		return
	}
	body := http.MaxBytesReader(c.W, c.R.Body, s.cfg.MaxUploadBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		c.JSONStatus(413, map[string]any{"code": 40005, "message": "Request entity too large"})
		return
	}
	contentType := c.R.Header.Get("Content-Type")
	if err := s.Store.SaveUpload(idv, s.cfg.UploadDir, data, contentType); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// handleCDN serves a stored attachment.
func (s *Server) handleCDN(c *Ctx) {
	path, err := s.Store.UploadPath(c.Param("uploadID"))
	if err != nil {
		http.NotFound(c.W, c.R)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(c.W, c.R)
		return
	}
	c.W.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(c.W, c.R, path)
}

func urlEscape(v string) string {
	replacer := strings.NewReplacer(" ", "%20", "#", "%23", "?", "%3F", "&", "%26", "%", "%25")
	return replacer.Replace(v)
}

// resolveAttachments fills URLs for staged attachment ids.
func (s *Server) resolveAttachments(channelID string, in []attachmentInput) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, a := range in {
		filename := filepath.Base(a.Filename)
		if filename == "" || filename == "." {
			filename = "file"
		}
		if a.ID == "" {
			a.ID = s.Store.NewID()
		}
		item := map[string]any{
			"id":        a.ID,
			"filename":  filename,
			"size":      0,
			"url":       s.cfg.CDNBase + "/cdn/attachments/" + channelID + "/" + a.ID + "/" + urlEscape(filename),
			"proxy_url": s.cfg.CDNBase + "/cdn/attachments/" + channelID + "/" + a.ID + "/" + urlEscape(filename),
		}
		if u, err := s.Store.Upload(a.ID); err == nil && u.ChannelID == channelID {
			item["size"] = u.Size
			if u.ContentType != nil {
				item["content_type"] = *u.ContentType
			}
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) audit(c *Ctx, guildID string, action int, targetID string, changes []store.AuditChange) {
	if guildID == "" {
		return
	}
	reason := decodeAuditReason(c.R.Header.Get("X-Audit-Log-Reason"))
	if err := s.Store.WriteAudit(guildID, c.User.ID, action, targetID, changes, reason); err != nil {
		s.cfg.Logger.Warn("audit log", "err", err)
	}
}

type attachmentInput struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
}
