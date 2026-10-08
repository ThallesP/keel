package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ThallesP/keel/internal/app"
)

// Raw (non-Huma) routes. Owner: the deploy area.
//
// POST /worker/events (convex/http.ts; docs/go/spec/swarm-worker.md §11.2): Docker events from the
// per-node agent, as one object, an array, or NDJSON. Bearer KEEL_WORKER_TOKEN. Plain-text
// answers, exactly as before: 401 unauthorized, 413 too large, 400 bad json, 200 ok.
func (s *Server) registerDeployRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /worker/events", s.workerEvents)
}

// workerEventsMaxBody: 256 KiB, counted in UTF-16 code units like the JS `text.length` check.
const workerEventsMaxBody = 256 * 1024

func (s *Server) workerEvents(w http.ResponseWriter, r *http.Request) {
	if !deployWorkerAuthorized(r, s.app.Config.WorkerToken) {
		deployWriteText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// A UTF-8 byte is at most one UTF-16 unit and a unit at most 3 bytes: more than 3× the limit
	// in bytes is always too large, and within that the exact unit count decides.
	body, err := io.ReadAll(io.LimitReader(r.Body, 3*workerEventsMaxBody+1))
	if err != nil {
		deployWriteText(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(body) > 3*workerEventsMaxBody || deployUTF16Length(body) > workerEventsMaxBody {
		deployWriteText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	events, err := parseWorkerEvents(string(body))
	if err != nil {
		deployWriteText(w, http.StatusBadRequest, "bad json")
		return
	}
	// `X-Keel-Resync: 1` comes with the agent's first batch after a (re)start.
	s.app.IngestWorkerEvents(r.Context(), events, r.Header.Get("X-Keel-Resync") == "1")
	deployWriteText(w, http.StatusOK, "ok")
}

// deployWorkerAuthorized: `Authorization: Bearer <KEEL_WORKER_TOKEN>` ("Bearer " is case-sensitive, the
// token is trimmed). An unset token rejects everything. Constant time, length included: both
// sides are hashed before the compare.
func deployWorkerAuthorized(r *http.Request, expected string) bool {
	header := r.Header.Get("Authorization")
	if expected == "" || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	got := strings.TrimFunc(strings.TrimPrefix(header, "Bearer "), deployJSSpace)
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func deployWriteText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

// deployUTF16Length is the JavaScript length of body decoded as UTF-8 (an invalid byte decodes to one
// U+FFFD, one unit).
func deployUTF16Length(body []byte) int {
	n := 0
	for len(body) > 0 {
		r, size := utf8.DecodeRune(body)
		body = body[size:]
		if w := utf16.RuneLen(r); w > 0 {
			n += w
		} else {
			n++
		}
	}
	return n
}

var errDeployNotDockerEvent = errors.New("not a Docker event")

// parseWorkerEvents is http.ts parseEvents + trim: text whose trimmed form starts with `[` is one
// JSON document; otherwise every non-blank line is one. A single object counts as a list of one.
// Every element must be an object with `Type` and `Action` keys.
func parseWorkerEvents(text string) ([]app.DockerEvent, error) {
	var list []any
	if strings.HasPrefix(strings.TrimFunc(text, deployJSSpace), "[") {
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return nil, err
		}
		if arr, ok := v.([]any); ok {
			list = arr
		} else {
			list = []any{v}
		}
	} else {
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimFunc(line, deployJSSpace) == "" {
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	}
	events := make([]app.DockerEvent, 0, len(list))
	for _, item := range list {
		e, ok := item.(map[string]any)
		if !ok {
			return nil, errDeployNotDockerEvent
		}
		typ, hasType := e["Type"]
		action, hasAction := e["Action"]
		if !hasType || !hasAction {
			return nil, errDeployNotDockerEvent
		}
		ev := app.DockerEvent{Type: deployJSString(typ), Action: deployJSString(action)}
		if actor, ok := e["Actor"].(map[string]any); ok {
			if attrs, ok := actor["Attributes"].(map[string]any); ok {
				ev.Name, _ = attrs["name"].(string)
				ev.ServiceName, _ = attrs["com.docker.swarm.service.name"].(string)
			}
		}
		if t, ok := e["time"].(float64); ok {
			ev.Time = &t
		}
		events = append(events, ev)
	}
	return events, nil
}

// deployJSString is JavaScript's String(v) for a parsed JSON value.
func deployJSString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		switch {
		case math.IsInf(x, 0) || math.IsNaN(x):
			return strconv.FormatFloat(x, 'g', -1, 64)
		case x == math.Trunc(x) && math.Abs(x) < 1e21:
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			if p != nil {
				parts[i] = deployJSString(p)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// deployJSSpace is ECMAScript's whitespace (String.prototype.trim).
func deployJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
