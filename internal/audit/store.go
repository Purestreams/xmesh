package audit

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const segmentBytes = 1 << 20
const maxArchives = 64

type Store struct {
	mu      sync.Mutex
	dir     string
	windows map[string]*window
}

func NewStore(dir string) *Store { return &Store{dir: dir, windows: map[string]*window{}} }

func LimitBytes(mb int) int64 {
	if mb <= 0 {
		mb = DefaultLimitMB
	}
	return int64(mb) * 1_000_000
}

func (s *Store) userDir(user string) (string, error) {
	if user == "" || len(user) > 128 {
		return "", errors.New("invalid audit user")
	}
	for _, c := range user {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "", errors.New("invalid audit user")
		}
	}
	return filepath.Join(s.dir, user), nil
}

func (s *Store) userWindow(user string) *window {
	w := s.windows[user]
	if w == nil {
		w = &window{}
		s.windows[user] = w
	}
	return w
}

// Append is independent of the Controller state JSON and serialized with reads,
// rotation and clearing. Node writers use bounded, nonblocking queues.
func (s *Store) Append(user string, limit int64, events []Event, dropped uint64, now time.Time) error {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.userDir(user)
	if err != nil {
		return err
	}
	w := s.userWindow(user)
	w.dropped += dropped
	if dropped > 0 {
		w.lastReceived = now
	}
	if len(events) == 0 {
		return nil
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// If old files cannot be pruned, refuse new writes rather than keep growing.
	if err = s.prune(dir, limit); err != nil {
		return err
	}
	path := filepath.Join(dir, "current.jsonl")
	for _, event := range events {
		event.At = event.At.UTC()
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		if len(line) > 16<<10 {
			return errors.New("audit event too large")
		}
		info, err := os.Stat(path)
		size := int64(0)
		if err == nil {
			size = info.Size()
		} else if !os.IsNotExist(err) {
			return err
		}
		threshold := int64(segmentBytes)
		if limit < threshold {
			threshold = limit
		}
		if size > 0 && size+int64(len(line)) > threshold {
			if err = s.rotate(dir); err != nil {
				return err
			}
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(line)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		w.add(event, now)
	}
	return s.prune(dir, limit)
}

func (s *Store) rotate(dir string) error {
	source, err := os.Open(filepath.Join(dir, "current.jsonl"))
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.CreateTemp(dir, ".archive-*.tmp")
	if err != nil {
		return err
	}
	temp := target.Name()
	defer os.Remove(temp)
	compressed := gzip.NewWriter(target)
	_, copyErr := io.Copy(compressed, source)
	gzipErr := compressed.Close()
	closeErr := target.Close()
	sourceErr := source.Close()
	if copyErr != nil {
		return copyErr
	}
	if gzipErr != nil {
		return gzipErr
	}
	if closeErr != nil {
		return closeErr
	}
	if sourceErr != nil {
		return sourceErr
	}
	name := "archive-" + time.Now().UTC().Format("20060102T150405.000000000") + "-" + strings.TrimSuffix(filepath.Base(temp), ".tmp") + ".jsonl.gz"
	if err = os.Rename(temp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, "current.jsonl"))
}

func files(dir string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []os.FileInfo
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.Name() != "current.jsonl" && !(strings.HasPrefix(entry.Name(), "archive-") && strings.HasSuffix(entry.Name(), ".jsonl.gz")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result, nil
}

func (s *Store) prune(dir string, limit int64) error {
	entries, err := files(dir)
	if err != nil {
		return err
	}
	var total int64
	archives := 0
	for _, entry := range entries {
		total += entry.Size()
		if entry.Name() != "current.jsonl" {
			archives++
		}
	}
	for _, entry := range entries {
		if total <= limit && archives <= maxArchives {
			break
		}
		if entry.Name() == "current.jsonl" {
			continue
		}
		if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
		total -= entry.Size()
		archives--
	}
	if total > limit {
		return errors.New("audit quota exceeded")
	}
	return nil
}

// Enforce applies a reduced quota even without new events.
func (s *Store) Enforce(user string, limit int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.userDir(user)
	if err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(dir, "current.jsonl"))
	if err == nil && info.Size() > limit {
		if err = s.rotate(dir); err != nil {
			return err
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.prune(dir, limit)
}

type Filter struct {
	Host   string
	Port   int
	Result string
	Limit  int
}
type View struct {
	Events       []Event `json:"events"`
	StorageBytes int64   `json:"storage_bytes"`
	LimitBytes   int64   `json:"limit_bytes"`
	Archives     int     `json:"archives"`
	Summary      Summary `json:"summary"`
}

func openLog(path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return file, nil
	}
	zipped, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return &gzipFile{Reader: zipped, file: file}, nil
}

type gzipFile struct {
	*gzip.Reader
	file *os.File
}

func (f *gzipFile) Close() error {
	err := f.Reader.Close()
	other := f.file.Close()
	if err != nil {
		return err
	}
	return other
}

func (s *Store) Read(user string, limit int64, filter Filter, now time.Time) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	view := View{Events: []Event{}, LimitBytes: limit, Summary: s.userWindow(user).summary(now)}
	dir, err := s.userDir(user)
	if err != nil {
		return view, err
	}
	entries, err := files(dir)
	if err != nil {
		return view, err
	}
	for _, entry := range entries {
		view.StorageBytes += entry.Size()
		if entry.Name() != "current.jsonl" {
			view.Archives++
		}
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 200
	}
	for i := len(entries) - 1; i >= 0 && len(view.Events) < filter.Limit; i-- {
		reader, err := openLog(filepath.Join(dir, entries[i].Name()))
		if err != nil {
			return view, err
		}
		scanner := bufio.NewScanner(io.LimitReader(reader, segmentBytes+16<<10))
		scanner.Buffer(make([]byte, 4096), 32<<10)
		// Keep only the most recent matching events in this bounded segment.
		matches := make([]Event, 0, filter.Limit)
		for scanner.Scan() {
			var event Event
			if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
				reader.Close()
				return view, fmt.Errorf("invalid audit log: %w", err)
			}
			if filter.Host != "" && !strings.Contains(strings.ToLower(event.Host), strings.ToLower(filter.Host)) && !strings.Contains(event.IP, filter.Host) {
				continue
			}
			if filter.Port != 0 && event.Port != filter.Port || filter.Result != "" && event.Result != filter.Result {
				continue
			}
			matches = append(matches, event)
			if len(matches) > filter.Limit {
				matches = matches[1:]
			}
		}
		scanErr := scanner.Err()
		reader.Close()
		if scanErr != nil {
			return view, scanErr
		}
		for j := len(matches) - 1; j >= 0 && len(view.Events) < filter.Limit; j-- {
			view.Events = append(view.Events, matches[j])
		}
	}
	return view, nil
}

func (s *Store) Clear(user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.userDir(user)
	if err != nil {
		return err
	}
	entries, err := files(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	delete(s.windows, user)
	return nil
}

func (s *Store) Export(user string, destination io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.userDir(user)
	if err != nil {
		return err
	}
	entries, err := files(dir)
	if err != nil {
		return err
	}
	zipped := gzip.NewWriter(destination)
	for _, entry := range entries {
		reader, err := openLog(filepath.Join(dir, entry.Name()))
		if err != nil {
			zipped.Close()
			return err
		}
		_, err = io.Copy(zipped, io.LimitReader(reader, segmentBytes+16<<10))
		reader.Close()
		if err != nil {
			zipped.Close()
			return err
		}
	}
	return zipped.Close()
}
