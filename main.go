package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/acoshift/pgsql/pgstmt"
	_ "github.com/lib/pq"
	"github.com/moonrhythm/parapet"
	"github.com/moonrhythm/parapet/pkg/upstream"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	upstreamHost := os.Getenv("UPSTREAM_HOST")
	upstreamScheme := os.Getenv("UPSTREAM_SCHEME")
	if upstreamHost == "" {
		log.Fatal("UPSTREAM_HOST is required")
	}
	if upstreamScheme == "" {
		upstreamScheme = "http"
	}

	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		log.Fatal("DB_URL is required")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	log.Println("reverse-proxy-logger-pg")
	log.Printf("listen: :%s", port)
	log.Printf("upstream: %s://%s", upstreamScheme, upstreamHost)

	var tr http.RoundTripper
	switch upstreamScheme {
	case "http":
		tr = &upstream.HTTPTransport{}
	case "https":
		tr = &upstream.HTTPSTransport{}
	default:
		log.Fatalf("unsupported upstream scheme: %s", upstreamScheme)
	}

	srv := parapet.NewBackend()
	srv.Addr = ":" + port
	srv.Use(&logger{db: db})
	srv.Use(&upstream.Upstream{
		Transport: tr,
		Host:      upstreamHost,
	})
	err = srv.ListenAndServe()
	if err != nil {
		log.Fatalf("failed to listen and serve: %v", err)
	}
}

type logger struct {
	db *sql.DB
	ch chan *logEntry
}

type logEntry struct {
	request  requestEntry
	response responseEntry
	ts       time.Time
}

type requestEntry struct {
	Method string      `json:"method"`
	Host   string      `json:"host"`
	URI    string      `json:"uri"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
}

type responseEntry struct {
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
}

func (l *logger) flushLoop() {
	buf := make([]*logEntry, 0, 100)

	flush := func() {
		if len(buf) == 0 {
			return
		}

		ctx := context.Background()
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		_, err := pgstmt.Insert(func(b pgstmt.InsertStatement) {
			b.Into("request_logs")
			b.Columns("request", "response", "ts")
			for _, x := range buf {
				b.Value(x.request, x.response, x.ts)
			}
		}).ExecContext(ctx, l.db.ExecContext)
		if err != nil {
			log.Printf("failed to insert [%d]: %v", len(buf), err)
			// do not return, continue to discard buffer
		}

		// always discard buffer even if error
		buf = buf[:0]
	}

	tickTime := time.Second
	ticker := time.NewTicker(tickTime)
	for {
		select {
		case e := <-l.ch:
			buf = append(buf, e)
			if len(buf) == cap(buf) {
				flush()
			}
		case <-ticker.C:
			flush()
		}
		ticker.Reset(tickTime)
	}
}

func (l *logger) ServeHandler(h http.Handler) http.Handler {
	if l.db == nil {
		panic("db is nil")
	}
	l.ch = make(chan *logEntry, 1000)
	go l.flushLoop()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := logEntry{}
		e.ts = time.Now()
		e.request.Method = r.Method
		e.request.Host = r.Host
		e.request.URI = r.RequestURI
		e.request.Header = r.Header

		var reqBody bytes.Buffer

		_, err := io.Copy(&reqBody, r.Body)
		if err != nil {
			log.Printf("failed to copy request body: %v", err)
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		e.request.Body = reqBody.String()
		r.Body = io.NopCloser(&reqBody)

		var resBody bytes.Buffer
		nw := newResponseWriter(w, &resBody)
		defer func() {
			e.response.Status = nw.status
			e.response.Header = nw.Header()
			e.response.Body = resBody.String()
			l.ch <- &e
		}()

		h.ServeHTTP(nw, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	wroteHeader bool
	status      int
	w           io.Writer
}

func newResponseWriter(w http.ResponseWriter, buf *bytes.Buffer) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		w:              io.MultiWriter(w, buf),
	}
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.w.Write(b)
}
