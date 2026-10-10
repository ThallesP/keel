package agent

import (
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	streamStdout = "stdout"
	streamStderr = "stderr"
)

type Frame struct {
	Stream string
	Text   string
}

type FrameParser struct {
	carry []byte
}

func (p *FrameParser) Push(chunk []byte) []Frame {
	buf := chunk
	if len(p.carry) > 0 {
		buf = make([]byte, 0, len(p.carry)+len(chunk))
		buf = append(append(buf, p.carry...), chunk...)
	}
	var out []Frame
	off := 0
	for off+8 <= len(buf) {
		typ := buf[off]
		n := int64(binary.BigEndian.Uint32(buf[off+4 : off+8]))
		if typ > 2 {
			out = append(out, Frame{Stream: streamStdout, Text: string(buf[off:])})
			p.carry = nil
			return out
		}
		if int64(off)+8+n > int64(len(buf)) {
			break
		}
		stream := streamStdout
		if typ == 2 {
			stream = streamStderr
		}
		out = append(out, Frame{Stream: stream, Text: string(buf[off+8 : off+8+int(n)])})
		off += 8 + int(n)
	}
	p.carry = append([]byte(nil), buf[off:]...)
	return out
}

type Line struct {
	Time   string
	Text   string
	Stream string
}

type LineSplitter struct {
	partial map[string]string
}

func (s *LineSplitter) Push(f Frame) []Line {
	if s.partial == nil {
		s.partial = map[string]string{}
	}
	parts := strings.Split(s.partial[f.Stream]+f.Text, "\n")
	s.partial[f.Stream] = parts[len(parts)-1]
	var out []Line
	for _, raw := range parts[:len(parts)-1] {
		if raw != "" {
			out = append(out, parseLine(raw, f.Stream))
		}
	}
	return out
}

func parseLine(raw, stream string) Line {
	line := strings.TrimSuffix(raw, "\r")
	if space := strings.IndexByte(line, ' '); space > 0 {
		stamp := line[:space]
		if len(stamp) >= 20 && strings.HasSuffix(stamp, "Z") && stamp[4] == '-' {
			return Line{Time: stamp, Text: line[space+1:], Stream: stream}
		}
	}
	return Line{Text: line, Stream: stream}
}

var stampRE = regexp.MustCompile(`^(.+?)(?:\.(\d{1,9}))?Z$`)

func sinceAfter(stamp string) string {
	m := stampRE.FindStringSubmatch(stamp)
	if m == nil {
		return ""
	}
	t, err := time.Parse(time.RFC3339, m[1]+"Z")
	if err != nil {
		return ""
	}
	secs := t.Unix()
	frac := m[2] + strings.Repeat("0", 9-len(m[2]))
	nanos, _ := strconv.ParseInt(frac, 10, 64)
	nanos++
	if nanos >= 1_000_000_000 {
		nanos -= 1_000_000_000
		secs++
	}
	return fmt.Sprintf("%d.%09d", secs, nanos)
}

func dockerSince(ms float64) string {
	nanos := math.Floor(math.Mod(ms, 1000)*1_000_000 + 0.5)
	nanos = math.Min(nanos, 999_999_999)
	return fmt.Sprintf("%d.%09d", int64(math.Floor(ms/1000)), int64(nanos))
}

func eventsSinceAfter(timeNano int64) string {
	ns := timeNano + 1
	return fmt.Sprintf("%d.%09d", ns/1_000_000_000, ns%1_000_000_000)
}
