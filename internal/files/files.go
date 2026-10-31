// Package files is the Files page's storage: the folders the owner named in
// [[files.locations]], each opened as an os.Root so no path, ".." or symbolic
// link reaches outside it (docs/design.md §8m).
//
// Paths are slash-separated and relative to their location; "" is the
// location's top. Nothing is ever overwritten: a rename or move onto an
// existing name is refused, an upload or restore takes the next free
// "name (2).ext". Deleting moves to the location's trash. Every change in a
// location runs under the service's lock.
package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vasilistotskas/homecloud/internal/config"
)

// Errors the API maps to its codes. Each is wrapped with what it is about.
var (
	// ErrNoLocation: no location has that name.
	ErrNoLocation = errors.New("no such location")
	// ErrUnavailable: the location's folder cannot be opened (a drive that is
	// not connected, a share that is not mounted).
	ErrUnavailable = errors.New("location unavailable")
	// ErrInvalid: a path or name the location cannot hold.
	ErrInvalid = errors.New("invalid name")
	// ErrNotFound: nothing at that path.
	ErrNotFound = errors.New("not found")
	// ErrExists: the name is taken.
	ErrExists = errors.New("already exists")
)

const (
	// Reserved is homecloud's own folder at the top of every location:
	// uploads in progress and the trash. Listings hide it; paths may not
	// name it.
	Reserved   = ".homecloud"
	uploadsDir = Reserved + "/uploads"
	trashDir   = Reserved + "/trash"
	// content and record are an item's and its record's names in its trash
	// folder.
	content = "content"
	record  = "record.json"

	maxNameBytes = 255
	dirPerm      = 0o755
	filePerm     = 0o644
	idBytes      = 8
	// maxFreeNames bounds the search for a free "name (n).ext".
	maxFreeNames = 10000
)

// Service performs every Files operation.
type Service struct {
	locations      []config.Location
	trashRetention time.Duration
	uploadExpiry   time.Duration
	diskFree       func(path string) (uint64, error)
	now            func() time.Time
	log            *slog.Logger

	// mu serializes every change, so the agent's own operations never race
	// for a name.
	mu sync.Mutex
}

// New returns the service for cfg's locations; diskFree reports the free
// bytes on a path's volume.
func New(cfg config.Files, diskFree func(path string) (uint64, error), now func() time.Time, log *slog.Logger) *Service {
	return &Service{
		locations:      slices.Clone(cfg.Locations),
		trashRetention: cfg.TrashRetention.Std(),
		uploadExpiry:   cfg.UploadExpiry.Std(),
		diskFree:       diskFree,
		now:            now,
		log:            log,
	}
}

// Location is a configured location and whether its folder can be opened.
type Location struct {
	Name      string
	Available bool
	// Free is the free space on its volume, when available.
	Free uint64
}

// TrashRetention is how long deleted items stay in the trash.
func (s *Service) TrashRetention() time.Duration { return s.trashRetention }

// Locations lists the locations in configuration order.
func (s *Service) Locations() []Location {
	out := make([]Location, 0, len(s.locations))
	for _, l := range s.locations {
		loc := Location{Name: l.Name}
		if free, err := s.diskFree(l.Path); err == nil {
			if info, statErr := os.Stat(l.Path); statErr == nil && info.IsDir() {
				loc.Available, loc.Free = true, free
			}
		}
		out = append(out, loc)
	}
	return out
}

// Entry is one file or folder in a listing.
type Entry struct {
	Name     string
	Dir      bool
	Size     int64
	Modified time.Time
}

// List lists the folder dir of location loc: folders and regular files only
// (symbolic links and devices are left out), folders first, then by name.
func (s *Service) List(loc, dir string) ([]Entry, error) {
	root, err := s.open(loc)
	if err != nil {
		return nil, err
	}
	defer root.Close() //nolint:errcheck // read-only
	rel, err := Clean(dir)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(osPath(rel))
	if err != nil {
		return nil, notFound(err, dir)
	}
	defer f.Close() //nolint:errcheck // read-only
	items, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read folder %q: %w", dir, err)
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		if rel == "" && strings.EqualFold(it.Name(), Reserved) {
			continue
		}
		if t := it.Type(); t != 0 && !t.IsDir() {
			continue
		}
		info, err := it.Info()
		if err != nil {
			continue // removed while listing
		}
		e := Entry{Name: it.Name(), Dir: it.IsDir(), Modified: info.ModTime()}
		if !e.Dir {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

// CreateFolder creates the folder name inside dir.
func (s *Service) CreateFolder(loc, dir, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	return s.change(loc, func(root *os.Root) error {
		parent, err := s.existingDir(root, dir)
		if err != nil {
			return err
		}
		target := path.Join(parent, name)
		if err := free(root, target); err != nil {
			return err
		}
		if err := root.Mkdir(osPath(target), dirPerm); err != nil {
			return fmt.Errorf("create folder: %w", err)
		}
		return nil
	})
}

// Rename gives the file or folder at p the name newName in the same folder.
func (s *Service) Rename(loc, p, newName string) error {
	if err := ValidName(newName); err != nil {
		return err
	}
	return s.change(loc, func(root *os.Root) error {
		src, err := s.existing(root, p)
		if err != nil {
			return err
		}
		dst := path.Join(path.Dir(src), newName)
		if dst == src {
			return nil
		}
		if err := freeFor(root, src, dst); err != nil {
			return err
		}
		if err := root.Rename(osPath(src), osPath(dst)); err != nil {
			return fmt.Errorf("rename: %w", err)
		}
		return nil
	})
}

// Move moves each of paths into the folder dest, in order, and stops at the
// first that cannot move; it returns how many moved.
func (s *Service) Move(loc string, paths []string, dest string) (int, error) {
	moved := 0
	err := s.change(loc, func(root *os.Root) error {
		to, err := s.existingDir(root, dest)
		if err != nil {
			return err
		}
		for _, p := range paths {
			src, err := s.existing(root, p)
			if err != nil {
				return err
			}
			if to == src || strings.HasPrefix(to+"/", src+"/") {
				return fmt.Errorf("%w: %q cannot move into itself", ErrInvalid, p)
			}
			dst := path.Join(to, path.Base(src))
			if dst == src {
				moved++
				continue
			}
			if err := freeFor(root, src, dst); err != nil {
				return err
			}
			if err := root.Rename(osPath(src), osPath(dst)); err != nil {
				return fmt.Errorf("move %q: %w", p, err)
			}
			moved++
		}
		return nil
	})
	return moved, err
}

// trashRecord is what the trash keeps beside a deleted item.
type trashRecord struct {
	Path    string    `json:"path"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	Deleted time.Time `json:"deleted"`
}

// Trash moves each of paths to the location's trash, in order, and stops at
// the first that cannot move; it returns how many moved.
func (s *Service) Trash(loc string, paths []string) (int, error) {
	moved := 0
	err := s.change(loc, func(root *os.Root) error {
		if err := root.MkdirAll(trashDir, dirPerm); err != nil {
			return fmt.Errorf("create the trash: %w", err)
		}
		for _, p := range paths {
			src, err := s.existing(root, p)
			if err != nil {
				return err
			}
			info, err := root.Lstat(osPath(src))
			if err != nil {
				return notFound(err, p)
			}
			id, err := newID()
			if err != nil {
				return err
			}
			slot := path.Join(trashDir, id)
			if err = root.Mkdir(slot, dirPerm); err != nil {
				return fmt.Errorf("create trash slot: %w", err)
			}
			rec := trashRecord{Path: src, Dir: info.IsDir(), Deleted: s.now().UTC()}
			if !rec.Dir {
				rec.Size = info.Size()
			}
			data, err := json.Marshal(rec)
			if err != nil {
				return fmt.Errorf("encode trash record: %w", err)
			}
			if err := root.WriteFile(path.Join(slot, record), data, filePerm); err != nil {
				return errors.Join(fmt.Errorf("write trash record: %w", err), root.RemoveAll(slot))
			}
			if err := root.Rename(osPath(src), path.Join(slot, content)); err != nil {
				return errors.Join(fmt.Errorf("move %q to the trash: %w", p, err), root.RemoveAll(slot))
			}
			moved++
		}
		return nil
	})
	return moved, err
}

// TrashItem is one item in a location's trash.
type TrashItem struct {
	ID      string
	Path    string
	Dir     bool
	Size    int64
	Deleted time.Time
}

// ListTrash lists a location's trash, most recently deleted first.
func (s *Service) ListTrash(loc string) ([]TrashItem, error) {
	root, err := s.open(loc)
	if err != nil {
		return nil, err
	}
	defer root.Close() //nolint:errcheck // read-only
	return readTrash(root)
}

func readTrash(root *os.Root) ([]TrashItem, error) {
	slots, err := fs.ReadDir(root.FS(), trashDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the trash: %w", err)
	}
	out := make([]TrashItem, 0, len(slots))
	for _, slot := range slots {
		rec, err := readRecord(root, slot.Name())
		if err != nil {
			continue // a slot whose move never completed; Purge removes it
		}
		out = append(out, TrashItem{ID: slot.Name(), Path: rec.Path, Dir: rec.Dir, Size: rec.Size, Deleted: rec.Deleted})
	}
	slices.SortFunc(out, func(a, b TrashItem) int { return b.Deleted.Compare(a.Deleted) })
	return out, nil
}

func readRecord(root *os.Root, id string) (trashRecord, error) {
	var rec trashRecord
	if !validID(id) {
		return rec, fmt.Errorf("%w: trash item %q", ErrNotFound, id)
	}
	data, err := root.ReadFile(path.Join(trashDir, id, record))
	if err != nil {
		return rec, notFound(err, "trash item "+id)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("trash item %s: %w", id, err)
	}
	if _, err := root.Lstat(path.Join(trashDir, id, content)); err != nil {
		return rec, notFound(err, "trash item "+id)
	}
	if _, err := Clean(rec.Path); err != nil || rec.Path == "" {
		return rec, fmt.Errorf("trash item %s: bad original path %q", id, rec.Path)
	}
	return rec, nil
}

// Restore puts each trash item back at its original path, recreating its
// folder if it is gone and taking the next free name if its own is taken. It
// stops at the first that cannot be restored and returns the paths restored.
func (s *Service) Restore(loc string, ids []string) ([]string, error) {
	var restored []string
	err := s.change(loc, func(root *os.Root) error {
		for _, id := range ids {
			rec, err := readRecord(root, id)
			if err != nil {
				return err
			}
			dir := path.Dir(rec.Path)
			if dir == "." {
				dir = ""
			}
			if dir != "" {
				if err = root.MkdirAll(osPath(dir), dirPerm); err != nil {
					return fmt.Errorf("recreate folder %q: %w", dir, err)
				}
			}
			dst, err := freeName(root, dir, path.Base(rec.Path))
			if err != nil {
				return err
			}
			slot := path.Join(trashDir, id)
			if err := root.Rename(path.Join(slot, content), osPath(dst)); err != nil {
				return fmt.Errorf("restore %q: %w", rec.Path, err)
			}
			if err := root.RemoveAll(slot); err != nil {
				s.log.Warn("restored item's trash slot not removed", "err", err)
			}
			restored = append(restored, dst)
		}
		return nil
	})
	return restored, err
}

// Purge removes trash items deleted longer than files.trash_retention ago and
// unfinished uploads untouched for files.upload_expiry, in every available
// location.
func (s *Service) Purge(ctx context.Context) {
	for _, l := range s.locations {
		if ctx.Err() != nil {
			return
		}
		err := s.change(l.Name, func(root *os.Root) error {
			return errors.Join(s.purgeTrash(root), s.purgeUploads(root))
		})
		if err != nil && !errors.Is(err, ErrUnavailable) {
			s.log.ErrorContext(ctx, "files location not cleaned up, retrying at the next pass", "location", l.Name, "err", err)
		}
	}
}

func (s *Service) purgeTrash(root *os.Root) error {
	slots, err := fs.ReadDir(root.FS(), trashDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the trash: %w", err)
	}
	var errs []error
	cutoff := s.now().Add(-s.trashRetention)
	for _, slot := range slots {
		rec, err := readRecord(root, slot.Name())
		var expired bool
		if err != nil {
			// A slot left by an interrupted delete: remove it once it is old.
			info, statErr := slot.Info()
			expired = statErr == nil && info.ModTime().Before(cutoff)
		} else {
			expired = rec.Deleted.Before(cutoff)
		}
		if expired {
			if err := root.RemoveAll(path.Join(trashDir, slot.Name())); err != nil {
				errs = append(errs, fmt.Errorf("purge trash item %s: %w", slot.Name(), err))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) purgeUploads(root *os.Root) error {
	items, err := fs.ReadDir(root.FS(), uploadsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read unfinished uploads: %w", err)
	}
	var errs []error
	cutoff := s.now().Add(-s.uploadExpiry)
	for _, it := range items {
		info, err := it.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := root.Remove(path.Join(uploadsDir, it.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove expired upload file %s: %w", it.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Open opens the regular file at p for reading.
func (s *Service) Open(loc, p string) (*os.File, fs.FileInfo, error) {
	root, err := s.open(loc)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close() //nolint:errcheck // read-only; the file stays open
	rel, err := Clean(p)
	if err != nil {
		return nil, nil, err
	}
	f, err := root.Open(osPath(rel))
	if err != nil {
		return nil, nil, notFound(err, p)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close() //nolint:errcheck // read-only
		return nil, nil, fmt.Errorf("%w: %q is not a file", ErrNotFound, p)
	}
	return f, info, nil
}

// UploadDir returns the absolute folder where uploads into loc are written
// until they finish, creating it.
func (s *Service) UploadDir(loc string) (string, error) {
	var dir string
	err := s.change(loc, func(root *os.Root) error {
		if err := root.MkdirAll(uploadsDir, dirPerm); err != nil {
			return fmt.Errorf("create the uploads folder: %w", err)
		}
		dir = root.Name() + string(os.PathSeparator) + osPath(uploadsDir)
		return nil
	})
	return dir, err
}

// CheckUpload checks, before an upload starts, that dir exists in loc and
// name is a valid name; the saved name may still differ (FinishUpload).
func (s *Service) CheckUpload(loc, dir, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	root, err := s.open(loc)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // read-only
	_, err = s.existingDir(root, dir)
	return err
}

// FinishUpload moves the finished upload id (its data file in UploadDir) to
// name in dir, or the next free name, removes tusd's record of it, and
// returns the path it was saved at.
func (s *Service) FinishUpload(loc, id, dir, name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	if !validUploadID(id) {
		return "", fmt.Errorf("%w: upload %q", ErrInvalid, id)
	}
	var saved string
	err := s.change(loc, func(root *os.Root) error {
		parent, err := s.existingDir(root, dir)
		if err != nil {
			return err
		}
		dst, err := freeName(root, parent, name)
		if err != nil {
			return err
		}
		if err := root.Rename(path.Join(uploadsDir, id), osPath(dst)); err != nil {
			return fmt.Errorf("save upload: %w", err)
		}
		if err := root.Remove(path.Join(uploadsDir, id+".info")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("finished upload's record not removed; it expires with files.upload_expiry", "err", err)
		}
		saved = dst
		return nil
	})
	return saved, err
}

// Clean checks a relative, slash-separated path and returns it cleaned; ""
// is the location's top.
func Clean(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if err := ValidName(part); err != nil {
			return "", fmt.Errorf("%w in path %q", err, p)
		}
	}
	if strings.EqualFold(parts[0], Reserved) {
		return "", fmt.Errorf("%w: %q is homecloud's own folder", ErrInvalid, Reserved)
	}
	return p, nil
}

// windowsReserved are device names Windows refuses as file names, with or
// without an extension.
var windowsReserved = []string{"CON", "PRN", "AUX", "NUL"}

// ValidName checks one file or folder name, so that a name valid on the NAS
// is valid on Windows too.
func ValidName(name string) error {
	bad := func(why string) error { return fmt.Errorf("%w: %q %s", ErrInvalid, name, why) }
	switch {
	case name == "" || name == "." || name == "..":
		return bad("is not a name")
	case len(name) > maxNameBytes:
		return bad(fmt.Sprintf("is longer than %d bytes", maxNameBytes))
	case !utf8.ValidString(name):
		return bad("is not valid UTF-8")
	case strings.ContainsFunc(name, func(r rune) bool { return strings.ContainsRune(`<>:"/\|?*`, r) || unicode.IsControl(r) }):
		return bad(`contains one of < > : " / \ | ? * or a control character`)
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return bad("ends in a dot or a space")
	}
	stem, _, _ := strings.Cut(name, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	if slices.Contains(windowsReserved, stem) ||
		(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
		return bad("is a device name on Windows")
	}
	return nil
}

// change runs fn on loc's root under the service's lock.
func (s *Service) change(loc string, fn func(*os.Root) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.open(loc)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // nothing buffered
	return fn(root)
}

func (s *Service) open(loc string) (*os.Root, error) {
	i := slices.IndexFunc(s.locations, func(l config.Location) bool { return l.Name == loc })
	if i < 0 {
		return nil, fmt.Errorf("%w: %q", ErrNoLocation, loc)
	}
	root, err := os.OpenRoot(s.locations[i].Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrUnavailable, loc, err)
	}
	return root, nil
}

// existing checks that p names a file or folder (not the top) and returns
// it cleaned.
func (s *Service) existing(root *os.Root, p string) (string, error) {
	rel, err := Clean(p)
	if err != nil {
		return "", err
	}
	if rel == "" {
		return "", fmt.Errorf("%w: the location itself cannot be changed", ErrInvalid)
	}
	if _, err := root.Lstat(osPath(rel)); err != nil {
		return "", notFound(err, p)
	}
	return rel, nil
}

// existingDir checks that dir names a folder (or the top) and returns it
// cleaned.
func (s *Service) existingDir(root *os.Root, dir string) (string, error) {
	rel, err := Clean(dir)
	if err != nil {
		return "", err
	}
	info, err := root.Stat(osPath(rel))
	if err != nil {
		return "", notFound(err, dir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %q is not a folder", ErrNotFound, dir)
	}
	return rel, nil
}

// free checks that nothing has the name p.
func free(root *os.Root, p string) error {
	_, err := root.Lstat(osPath(p))
	switch {
	case err == nil:
		return fmt.Errorf("%w: %q", ErrExists, p)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("check %q: %w", p, err)
	}
}

// freeFor checks that dst is free for src: nothing has the name, or only src
// itself (a change of case on a case-insensitive file system).
func freeFor(root *os.Root, src, dst string) error {
	dstInfo, err := root.Lstat(osPath(dst))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check %q: %w", dst, err)
	}
	if srcInfo, srcErr := root.Lstat(osPath(src)); srcErr == nil && os.SameFile(srcInfo, dstInfo) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrExists, dst)
}

// freeName returns dir/name, or dir/"stem (n).ext" with the smallest free n
// from 2.
func freeName(root *os.Root, dir, name string) (string, error) {
	try := path.Join(dir, name)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // ".bashrc": the whole name is the stem
		stem, ext = name, ""
	}
	for n := 2; n <= maxFreeNames; n++ {
		switch err := free(root, try); {
		case err == nil:
			return try, nil
		case !errors.Is(err, ErrExists):
			return "", err
		}
		candidate := stem + " (" + strconv.Itoa(n) + ")" + ext
		if err := ValidName(candidate); err != nil {
			return "", err
		}
		try = path.Join(dir, candidate)
	}
	return "", fmt.Errorf("%w: no free name for %q", ErrExists, name)
}

func notFound(err error, p string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %q", ErrNotFound, p)
	}
	return fmt.Errorf("%q: %w", p, err)
}

// osPath turns a cleaned slash path into one os.Root accepts; "" is ".".
func osPath(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("trash id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func validID(id string) bool {
	if len(id) != 2*idBytes {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// validUploadID accepts tusd's upload ids (128 random bits in lowercase hex,
// its internal/uid package) and nothing that could name another file.
func validUploadID(id string) bool {
	return len(id) == 32 && !strings.ContainsFunc(id, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f')
	})
}
