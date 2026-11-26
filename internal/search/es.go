package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ESConfig configures the Elasticsearch/OpenSearch adapter. Guild messages live
// in one index per guild ("guild-sharded"), which mirrors the published
// cell/indices design at a single-cluster scale.
type ESConfig struct {
	// URLs is the single-cell cluster list. Cells overrides it with independent
	// Elasticsearch clusters; guilds are assigned to a cell by hash.
	URLs          []string
	Cells         [][]string
	Username      string
	Password      string
	Prefix        string
	Shards        int
	Replicas      int
	BatchSize     int
	FlushInterval time.Duration
}

// ES is an Index backed by the Elasticsearch REST API. Writes are queued to a
// bulk-indexing worker; reads are synchronous.
type esCell struct {
	urls []string
	next atomic.Uint64
}

type ES struct {
	cfg    ESConfig
	client *http.Client
	next   atomic.Uint64
	cells  []esCell

	queue   chan bulkOp
	done    chan struct{}
	wg      sync.WaitGroup
	ensured sync.Map
	indices sync.Map
	once    sync.Once
	mu      sync.Mutex
	err     error
}

type indexedDoc struct {
	cell  int
	index string
}

type bulkOp struct {
	cell    int
	index   string
	id      string
	doc     *Doc
	delete  bool
	barrier chan struct{}
}

// NewES builds the adapter and starts the indexing worker.
func NewES(cfg ESConfig) (*ES, error) {
	if len(cfg.URLs) == 0 && len(cfg.Cells) == 0 {
		return nil, fmt.Errorf("search: es urls required")
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "waveycord"
	}
	if cfg.Shards <= 0 {
		cfg.Shards = 1
	}
	if cfg.Replicas < 0 {
		cfg.Replicas = 0
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 500 * time.Millisecond
	}
	e := &ES{
		cfg: cfg, client: &http.Client{Timeout: 10 * time.Second},
		queue: make(chan bulkOp, 4096), done: make(chan struct{}),
	}
	if len(cfg.Cells) > 0 {
		for _, urls := range cfg.Cells {
			if len(urls) == 0 {
				continue
			}
			e.cells = append(e.cells, esCell{urls: urls})
		}
	}
	if len(e.cells) == 0 {
		e.cells = []esCell{{urls: cfg.URLs}}
	}
	e.wg.Add(1)
	go e.worker()
	return e, nil
}

// IndexName is the per-guild index this adapter writes to.
func (e *ES) IndexName(guildID string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, guildID)
	// Elasticsearch requires lowercase index names.
	return strings.ToLower(e.cfg.Prefix + "-guild-" + safe)
}

// CellIndex reports which cell owns a guild.
func (e *ES) CellIndex(guildID string) int { return e.cellFor(guildID) }

// CellCount reports the configured cell count.
func (e *ES) CellCount() int { return len(e.cells) }

// cellFor assigns a guild to a cell by hash.
func (e *ES) cellFor(guildID string) int {
	if len(e.cells) <= 1 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(guildID))
	return int(h.Sum64() % uint64(len(e.cells)))
}

func (e *ES) baseURL(cell int) string {
	c := &e.cells[cell]
	i := c.next.Add(1)
	return strings.TrimRight(c.urls[int(i)%len(c.urls)], "/")
}

func (e *ES) do(ctx context.Context, cell int, method, path string, body []byte, contentType string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL(cell)+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if e.cfg.Username != "" {
		req.SetBasicAuth(e.cfg.Username, e.cfg.Password)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, out, nil
}

func (e *ES) ensureIndex(ctx context.Context, cell int, index string) error {
	key := fmt.Sprintf("%d/%s", cell, index)
	if _, ok := e.ensured.Load(key); ok {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"settings": map[string]any{
			"number_of_shards":   e.cfg.Shards,
			"number_of_replicas": e.cfg.Replicas,
		},
		"mappings": map[string]any{"properties": map[string]any{
			"message_id": map[string]any{"type": "keyword"},
			"guild_id":   map[string]any{"type": "keyword"},
			"channel_id": map[string]any{"type": "keyword"},
			"author_id":  map[string]any{"type": "keyword"},
			"content":    map[string]any{"type": "text"},
			"created_at": map[string]any{"type": "long"},
		}},
	})
	status, out, err := e.do(ctx, cell, http.MethodPut, "/"+url.PathEscape(index), body, "application/json")
	if err != nil {
		return err
	}
	if status >= 300 && !bytes.Contains(out, []byte("resource_already_exists_exception")) {
		return fmt.Errorf("search: create index %s: status %d: %s", index, status, truncate(out))
	}
	e.ensured.Store(key, true)
	return nil
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300])
	}
	return string(b)
}

// Index enqueues a document for the bulk worker.
func (e *ES) Index(ctx context.Context, d Doc) error {
	if d.GuildID == "" {
		return nil
	}
	cell := e.cellFor(d.GuildID)
	index := e.IndexName(d.GuildID)
	if err := e.ensureIndex(ctx, cell, index); err != nil {
		return err
	}
	e.indices.Store(d.MessageID, indexedDoc{cell: cell, index: index})
	select {
	case e.queue <- bulkOp{cell: cell, index: index, id: d.MessageID, doc: &d}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Delete enqueues a delete for the bulk worker.
func (e *ES) Delete(ctx context.Context, messageID string) error {
	// The guild index is not known from an id alone; search across the prefix is
	// avoided by storing the delete against every known index is wasteful, so the
	// worker deletes by query against the prefix when id-only.
	e.indices.Delete(messageID)
	select {
	case e.queue <- bulkOp{id: messageID, delete: true}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *ES) worker() {
	defer e.wg.Done()
	batch := make([]bulkOp, 0, e.cfg.BatchSize)
	timer := time.NewTimer(e.cfg.FlushInterval)
	defer timer.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = e.bulk(batch)
		batch = batch[:0]
	}
	for {
		select {
		case op := <-e.queue:
			if op.barrier != nil {
				flush()
				close(op.barrier)
				continue
			}
			batch = append(batch, op)
			if len(batch) >= e.cfg.BatchSize {
				flush()
				timer.Reset(e.cfg.FlushInterval)
			}
		case <-timer.C:
			flush()
			timer.Reset(e.cfg.FlushInterval)
		case <-e.done:
			flush()
			return
		}
	}
}

func (e *ES) bulk(ops []bulkOp) error {
	type cellBatch struct {
		buf  bytes.Buffer
		cell int
	}
	batches := map[int]*cellBatch{}
	encByCell := func(cell int) *json.Encoder {
		b := batches[cell]
		if b == nil {
			b = &cellBatch{cell: cell}
			batches[cell] = b
		}
		return json.NewEncoder(&b.buf)
	}
	for _, op := range ops {
		if op.delete {
			body, _ := json.Marshal(map[string]any{"query": map[string]any{
				"term": map[string]any{"message_id": op.id},
			}})
			index := e.cfg.Prefix + "-guild-*"
			cell := -1
			if known, ok := e.indices.Load(op.id); ok {
				doc := known.(indexedDoc)
				index, cell = doc.index, doc.cell
			}
			if cell >= 0 {
				_, _, _ = e.do(context.Background(), cell, http.MethodPost,
					"/"+url.PathEscape(index)+"/_delete_by_query?conflicts=proceed&refresh=true", body, "application/json")
			} else {
				for ci := range e.cells {
					_, _, _ = e.do(context.Background(), ci, http.MethodPost,
						"/"+url.PathEscape(index)+"/_delete_by_query?conflicts=proceed&refresh=true", body, "application/json")
				}
			}
			continue
		}
		enc := encByCell(op.cell)
		meta := map[string]any{"index": map[string]any{"_index": op.index, "_id": op.id}}
		if err := enc.Encode(meta); err != nil {
			continue
		}
		doc := op.doc
		source := map[string]any{
			"message_id": doc.MessageID, "guild_id": doc.GuildID, "channel_id": doc.ChannelID,
			"author_id": doc.AuthorID, "content": doc.Content, "created_at": doc.CreatedAt,
		}
		if err := enc.Encode(source); err != nil {
			continue
		}
	}
	for _, batch := range batches {
		if batch.buf.Len() == 0 {
			continue
		}
		status, out, err := e.do(context.Background(), batch.cell, http.MethodPost, "/_bulk?refresh=false",
			batch.buf.Bytes(), "application/x-ndjson")
		if err != nil {
			e.setErr(err)
			return err
		}
		if status >= 300 {
			err := fmt.Errorf("search: bulk status %d: %s", status, truncate(out))
			e.setErr(err)
			return err
		}
		var parsed struct {
			Errors bool                        `json:"errors"`
			Items  []map[string]map[string]any `json:"items"`
		}
		if json.Unmarshal(out, &parsed) == nil && parsed.Errors {
			e.setErr(fmt.Errorf("search: bulk reported item errors"))
		}
	}
	return nil
}

func (e *ES) setErr(err error) {
	e.mu.Lock()
	e.err = err
	e.mu.Unlock()
}

// Search runs a guild-scoped query against that guild's index. Permission
// filtering is applied as a channel filter inside the index query.
func (e *ES) Search(ctx context.Context, q Query) (Result, error) {
	limit := q.Limit
	if limit <= 0 || limit > 25 {
		limit = 25
	}
	must := []any{map[string]any{"match": map[string]any{"content": q.Content}}}
	filter := []any{}
	if len(q.Channels) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"channel_id": q.Channels}})
	}
	if q.AuthorID != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"author_id": q.AuthorID}})
	}
	body, _ := json.Marshal(map[string]any{
		"size":             limit,
		"track_total_hits": true,
		"query":            map[string]any{"bool": map[string]any{"must": must, "filter": filter}},
		"sort":             []any{"_score", map[string]any{"created_at": "desc"}},
		"_source":          []string{"message_id"},
	})
	status, out, err := e.do(ctx, e.cellFor(q.GuildID), http.MethodPost,
		"/"+url.PathEscape(e.IndexName(q.GuildID))+"/_search", body, "application/json")
	if err != nil {
		return Result{}, err
	}
	if status == http.StatusNotFound && bytes.Contains(out, []byte("index_not_found_exception")) {
		// A guild with no indexed messages has no index yet; that is an empty
		// result, not a search failure.
		return Result{MessageIDs: []string{}}, nil
	}
	if status >= 300 {
		return Result{}, fmt.Errorf("search: query status %d: %s", status, truncate(out))
	}
	var parsed struct {
		Hits struct {
			Total any `json:"total"`
			Hits  []struct {
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return Result{}, err
	}
	res := Result{MessageIDs: []string{}}
	switch total := parsed.Hits.Total.(type) {
	case float64:
		res.Total = int(total)
	case map[string]any:
		if v, ok := total["value"].(float64); ok {
			res.Total = int(v)
		}
	}
	for _, hit := range parsed.Hits.Hits {
		res.MessageIDs = append(res.MessageIDs, hit.ID)
	}
	return res, nil
}

// Flush drains the queue and returns once the batch has been sent.
func (e *ES) Flush(ctx context.Context) error {
	ack := make(chan struct{})
	select {
	case e.queue <- bulkOp{barrier: ack}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
	case <-ctx.Done():
		return ctx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// Close drains and stops the worker.
func (e *ES) Close() error {
	e.once.Do(func() { close(e.done) })
	e.wg.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// Reset deletes every guild index under the adapter prefix, so a subsequent
// reindex rebuilds from the database.
func (e *ES) Reset(ctx context.Context) error {
	// The wildcard must stay literal; only the prefix is escaped.
	for ci := range e.cells {
		status, out, err := e.do(ctx, ci, http.MethodDelete, "/"+url.PathEscape(e.cfg.Prefix)+"-guild-*?ignore_unavailable=true", nil, "")
		if err != nil {
			return err
		}
		if status >= 300 {
			return fmt.Errorf("search: reset status %d: %s", status, truncate(out))
		}
	}
	e.ensured = sync.Map{}
	return nil
}

// Health pings the cluster.
func (e *ES) Health(ctx context.Context) error {
	for ci := range e.cells {
		status, _, err := e.do(ctx, ci, http.MethodGet, "/_cluster/health", nil, "")
		if err != nil {
			return err
		}
		if status >= 300 {
			return fmt.Errorf("search: cluster health status %d", status)
		}
	}
	return nil
}
