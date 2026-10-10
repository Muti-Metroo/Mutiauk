package expose

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// folderHandler serves files from Root over a single connection.
// Strict policy: only an exact path → regular file mapping returns
// content. Anything else (missing, directory, symlink-out-of-root,
// path containing "..") returns an empty 404. No index.html fallback,
// no autoindex, no welcome page.
//
// We process requests directly on the connection rather than handing
// it to http.Server because the latter's accept loop has nowhere to
// go on a single-shot connection — closing it cleanly while letting
// in-flight requests complete is awkward. The bare-bones loop below
// supports HTTP/1.1 keep-alive (multiple requests per stream) and
// nothing else, which is all the static-file use case needs.
type folderHandler struct {
	root         string
	logger       *zap.Logger
	readTimeout  time.Duration
	writeTimeout time.Duration
}

func newFolderHandler(root string, logger *zap.Logger) (*folderHandler, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &folderHandler{
		root:         resolved,
		logger:       logger,
		readTimeout:  30 * time.Second,
		writeTimeout: 60 * time.Second,
	}, nil
}

// Serve processes HTTP requests on conn until the peer closes, an
// error occurs, or the context is cancelled.
func (h *folderHandler) Serve(ctx context.Context, conn net.Conn) error {
	defer conn.Close()

	// Close the conn when the context is cancelled so any in-flight
	// Read/Write returns immediately.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	br := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(h.readTimeout))
		req, err := http.ReadRequest(br)
		if err != nil {
			if errors.Is(err, io.EOF) ||
				errors.Is(err, net.ErrClosed) ||
				errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			// Best-effort 400 then close.
			_ = conn.SetWriteDeadline(time.Now().Add(h.writeTimeout))
			_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
			return err
		}

		// Drain the request body before responding so keep-alive can
		// reuse the connection.
		if req.Body != nil {
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
		}

		_ = conn.SetWriteDeadline(time.Now().Add(h.writeTimeout))
		closeAfter := req.Close || !req.ProtoAtLeast(1, 1)
		writer := newRespWriter(conn)
		h.serveHTTP(writer, req)
		if err := writer.flush(closeAfter); err != nil {
			return err
		}
		if closeAfter {
			return nil
		}
	}
}

func (h *folderHandler) serveHTTP(w *respWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.writeStatus(http.StatusNotFound)
		return
	}

	urlPath := r.URL.Path
	if urlPath == "" || strings.ContainsRune(urlPath, 0) {
		w.writeStatus(http.StatusNotFound)
		return
	}
	cleaned := path.Clean(urlPath)
	if cleaned == "." || cleaned == "/" || strings.HasSuffix(urlPath, "/") {
		w.writeStatus(http.StatusNotFound)
		return
	}
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
		w.writeStatus(http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(cleaned, "/") {
		w.writeStatus(http.StatusNotFound)
		return
	}

	joined := filepath.Join(h.root, filepath.FromSlash(cleaned))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		w.writeStatus(http.StatusNotFound)
		return
	}
	rel, err := filepath.Rel(h.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		w.writeStatus(http.StatusNotFound)
		return
	}

	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		w.writeStatus(http.StatusNotFound)
		return
	}

	f, err := os.Open(resolved)
	if err != nil {
		w.writeStatus(http.StatusNotFound)
		return
	}
	w.headers["Content-Type"] = contentTypeByExt(resolved)
	w.headers["Last-Modified"] = info.ModTime().UTC().Format(http.TimeFormat)
	w.headers["Content-Length"] = fmt.Sprintf("%d", info.Size())
	w.status = http.StatusOK
	if r.Method == http.MethodHead {
		f.Close()
		return
	}
	w.file = f
}

func contentTypeByExt(p string) string {
	ext := strings.ToLower(filepath.Ext(p))
	if mime := mimeForExt(ext); mime != "" {
		return mime
	}
	return "application/octet-stream"
}

// mimeForExt maps a small set of common extensions to MIME types so
// the handler doesn't depend on the host's mime.types file. Returns
// "" for unknown extensions; callers fall back to octet-stream.
func mimeForExt(ext string) string {
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".txt", ".md":
		return "text/plain; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	}
	return ""
}

// respWriter is a minimal HTTP/1.1 response builder. The body is
// either an in-memory []byte (for empty 404s) or a *os.File streamed
// during flush (for served files); never both.
type respWriter struct {
	conn    net.Conn
	status  int
	headers map[string]string
	body    []byte
	file    *os.File
}

func newRespWriter(conn net.Conn) *respWriter {
	return &respWriter{
		conn:    conn,
		status:  http.StatusOK,
		headers: make(map[string]string),
	}
}

func (w *respWriter) writeStatus(code int) {
	w.status = code
	w.body = nil
	w.file = nil
	if code == http.StatusNotFound {
		w.body = apache404Body
		w.headers["Content-Type"] = "text/html; charset=iso-8859-1"
		w.headers["Content-Length"] = fmt.Sprintf("%d", len(apache404Body))
		return
	}
	w.headers["Content-Length"] = "0"
}

// apache404Body matches the body Apache httpd serves for an
// unconfigured 404. We mirror it byte-for-byte (minus the trailing
// <address> banner that would leak server identity) so the response
// looks indistinguishable from a generic Apache install.
var apache404Body = []byte(`<!DOCTYPE HTML PUBLIC "-//IETF//DTD HTML 2.0//EN">
<html><head>
<title>404 Not Found</title>
</head><body>
<h1>Not Found</h1>
<p>The requested URL was not found on this server.</p>
</body></html>
`)

func (w *respWriter) flush(closeAfter bool) error {
	if w.file != nil {
		defer w.file.Close()
	}
	if _, ok := w.headers["Content-Length"]; !ok {
		w.headers["Content-Length"] = fmt.Sprintf("%d", len(w.body))
	}
	w.headers["Date"] = time.Now().UTC().Format(http.TimeFormat)
	if closeAfter {
		w.headers["Connection"] = "close"
	} else {
		w.headers["Connection"] = "keep-alive"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", w.status, http.StatusText(w.status))
	for k, v := range w.headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(w.conn, b.String()); err != nil {
		return err
	}
	if w.file != nil {
		if _, err := io.Copy(w.conn, w.file); err != nil {
			return err
		}
		return nil
	}
	if w.body != nil {
		if _, err := w.conn.Write(w.body); err != nil {
			return err
		}
	}
	return nil
}
