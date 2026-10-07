package iphonecopy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// A backup reaches an archive on another device (a NAS) through an incoming
// folder both can reach, one subfolder per device (its UDID):
//
//	archived-<stamp>.sha256   the manifest of the snapshot the archive
//	                          builds the next one on (Publish)
//	.push-<stamp>/, .push-<stamp>.sha256   a push being written
//	<stamp>/, <stamp>.sha256  a pushed backup: the files the archive does
//	                          not hold yet, and the whole backup's manifest
//	<stamp>.sig               the push's signature (PushKey)
//
// The computer with the backups (Push) sends only files whose SHA-256 differs
// from the published manifest; the archive (Ingest) takes in only pushes
// signed with its push key, links the rest from that snapshot, checks every
// file it receives against the manifest, and checks the copy's own plists
// before keeping it.
const (
	archivedPrefix = "archived-"
	pushPrefix     = ".push-"
)

// PushOutcome is what Push did.
type PushOutcome string

// Push outcomes.
const (
	// PushSent: the backup was pushed.
	PushSent PushOutcome = "sent"
	// PushPending: the backup was pushed before and the archive has not
	// taken it in yet.
	PushPending PushOutcome = "pending"
	// PushArchived: the archive holds this backup already.
	PushArchived PushOutcome = "archived"
)

// PushResult reports one Push.
type PushResult struct {
	Outcome PushOutcome
	Stamp   string
	// Files is the backup's; Sent and SentBytes what was written to incoming.
	Files     int
	Sent      int
	SentBytes int64
}

// Push sends the finished backup b (a device folder Apple's tool wrote on
// this computer) to the incoming folder of an archive on another device. An
// interrupted push is resumed: files already there with the right SHA-256
// are not sent again. The push is committed only if b's folder still holds
// the same finished backup afterwards (ErrChanged otherwise), and is signed
// with key.
func Push(ctx context.Context, b iphonebackup.Backup, incoming string, key PushKey) (PushResult, error) {
	if b.InProgress || b.Err != nil || b.CompletedAt.IsZero() {
		return PushResult{}, fmt.Errorf("%s holds no finished backup to push", b.Dir)
	}
	if err := ValidUDID(b.UDID); err != nil {
		return PushResult{}, err
	}
	in, err := os.OpenRoot(incoming)
	if err != nil {
		return PushResult{}, fmt.Errorf("open %s: %w", incoming, err)
	}
	defer in.Close() //nolint:errcheck // nothing to flush
	if mkErr := in.MkdirAll(b.UDID, dirMode); mkErr != nil {
		return PushResult{}, fmt.Errorf("create %s in %s: %w", b.UDID, incoming, mkErr)
	}
	stamp := Stamp(b.CompletedAt)
	res := PushResult{Stamp: stamp}

	dev, err := readIncoming(in, b.UDID)
	if err != nil {
		return PushResult{}, err
	}
	switch {
	case dev.archived == stamp:
		res.Outcome = PushArchived
		return res, nil
	case slices.Contains(dev.pushed, stamp):
		res.Outcome = PushPending
		return res, nil
	}
	for _, other := range dev.partial {
		if other != stamp {
			if rmErr := removePush(in, b.UDID, pushPrefix+other); rmErr != nil {
				return PushResult{}, rmErr
			}
		}
	}
	// A push interrupted between its commit's two renames left its folder
	// without the manifest: it was never committed, and is sent again.
	for _, other := range dev.uncommitted {
		if rmErr := removePush(in, b.UDID, other); rmErr != nil {
			return PushResult{}, rmErr
		}
	}
	var base Manifest
	if dev.archived != "" {
		if base, err = readManifestFile(in, b.UDID+"/"+archivedPrefix+dev.archived+manifestExt); err != nil {
			return PushResult{}, err
		}
	}

	src, err := os.OpenRoot(b.Dir)
	if err != nil {
		return PushResult{}, fmt.Errorf("open %s: %w", b.Dir, err)
	}
	defer src.Close() //nolint:errcheck // read-only
	m, err := HashTree(ctx, src)
	if err != nil {
		return PushResult{}, err
	}
	res.Files = len(m)

	partial := b.UDID + "/" + pushPrefix + stamp
	buf := make([]byte, readBuffer)
	for _, rel := range slices.Sorted(maps.Keys(m)) {
		if err := ctx.Err(); err != nil {
			return PushResult{}, fmt.Errorf("push cancelled: %w", err)
		}
		if d, ok := base[rel]; ok && d == m[rel] {
			continue
		}
		dst := partial + "/" + rel
		if d, err := hashFile(in, dst, buf); err == nil && d == m[rel] {
			continue // sent by an interrupted push
		}
		if err := in.MkdirAll(path.Dir(dst), dirMode); err != nil {
			return PushResult{}, fmt.Errorf("create %s: %w", path.Dir(dst), err)
		}
		n, err := sendFile(src, in, rel, dst, m[rel])
		if errors.Is(err, ErrMismatch) {
			return PushResult{}, fmt.Errorf("%w: %w", ErrChanged, err)
		}
		if err != nil {
			return PushResult{}, err
		}
		res.Sent++
		res.SentBytes += n
	}
	if err := unchanged(ctx, b); err != nil {
		return PushResult{}, err
	}
	if err := in.MkdirAll(partial, dirMode); err != nil { // a push that sends nothing
		return PushResult{}, fmt.Errorf("create %s: %w", partial, err)
	}
	pending := partial + manifestExt
	if err := writeFile(in, pending, m.Bytes()); err != nil {
		return PushResult{}, err
	}
	// The manifest commits the push, so its signature is in place first.
	if err := writeFile(in, b.UDID+"/"+stamp+signatureExt, key.signature(b.UDID, stamp, m.Bytes())); err != nil {
		return PushResult{}, err
	}
	if err := in.Rename(partial, b.UDID+"/"+stamp); err != nil {
		return PushResult{}, fmt.Errorf("commit push %s: %w", stamp, err)
	}
	if err := in.Rename(pending, b.UDID+"/"+stamp+manifestExt); err != nil {
		return PushResult{}, fmt.Errorf("commit push %s: %w", stamp, err)
	}
	res.Outcome = PushSent
	return res, nil
}

// sendFile copies rel from src to dst in in, replacing a partial file, and
// checks the data read against want.
func sendFile(src, in *os.Root, rel, dst string, want Digest) (int64, error) {
	a := Archive{root: in}
	if err := in.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("replace %s: %w", dst, err)
	}
	return a.copyFile(func(rel string) (io.ReadCloser, error) { return src.Open(rel) }, rel, dst, want)
}

// incomingDevice is what a device's incoming folder holds.
type incomingDevice struct {
	archived    string   // stamp of the published manifest, "" if none
	pushed      []string // committed pushes, oldest first
	partial     []string // pushes being written
	uncommitted []string // pushes renamed into place without their manifest
}

func readIncoming(in *os.Root, udid string) (incomingDevice, error) {
	entries, err := fs.ReadDir(in.FS(), udid)
	if err != nil {
		return incomingDevice{}, fmt.Errorf("list %s in %s: %w", udid, in.Name(), err)
	}
	var dev incomingDevice
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, archivedPrefix) && strings.HasSuffix(name, manifestExt):
			dev.archived = strings.TrimSuffix(strings.TrimPrefix(name, archivedPrefix), manifestExt)
		case strings.HasPrefix(name, pushPrefix) && e.IsDir():
			dev.partial = append(dev.partial, strings.TrimPrefix(name, pushPrefix))
		case strings.HasSuffix(name, manifestExt) && stampPattern.MatchString(strings.TrimSuffix(name, manifestExt)):
			if stamp := strings.TrimSuffix(name, manifestExt); names[stamp] {
				dev.pushed = append(dev.pushed, stamp)
			}
		case e.IsDir() && stampPattern.MatchString(name) && !names[name+manifestExt]:
			dev.uncommitted = append(dev.uncommitted, name)
		}
	}
	slices.Sort(dev.pushed)
	return dev, nil
}

func readManifestFile(root *os.Root, name string) (Manifest, error) {
	raw, err := readSmall(root, name, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

// readSmall reads a file of at most limit bytes from root.
func readSmall(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return data, nil
}

// removePush removes a push (its folder and manifest) from a device's
// incoming folder.
func removePush(in *os.Root, udid, name string) error {
	if err := in.RemoveAll(udid + "/" + name); err != nil {
		return fmt.Errorf("remove %s/%s: %w", udid, name, err)
	}
	for _, ext := range []string{manifestExt, signatureExt} {
		if err := in.Remove(udid + "/" + name + ext); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s/%s%s: %w", udid, name, ext, err)
		}
	}
	return nil
}

// Ingested reports one pushed backup the archive took in, or refused.
type Ingested struct {
	UDID  string
	Stamp string
	// Snapshot and Stats are set when it was taken in; Err says why not. A
	// refused push is removed, so the next push sends what is missing.
	Snapshot Snapshot
	Stats    Stats
	Err      error
}

// Ingest takes every pushed backup in the incoming folder into the archive,
// oldest first, each based on the snapshot base returns for its device (nil:
// copy everything), then publishes the base of the next push. Pushes taken
// in, refused or already in the archive are removed from incoming; a push
// the archive volume has no room for is kept, to be taken in later. Only
// pushes signed with key are taken in; now dates the check of their plists.
func (a *Archive) Ingest(ctx context.Context, incoming string, key PushKey, now time.Time,
	base func(udid string) (*Snapshot, error),
) ([]Ingested, error) {
	in, err := os.OpenRoot(incoming)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", incoming, err)
	}
	defer in.Close() //nolint:errcheck // nothing to flush
	entries, err := fs.ReadDir(in.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", incoming, err)
	}
	var out []Ingested
	for _, e := range entries {
		udid := e.Name()
		if !e.IsDir() || ValidUDID(udid) != nil {
			continue
		}
		dev, err := readIncoming(in, udid)
		if err != nil {
			return out, err
		}
		for _, stamp := range dev.pushed {
			r, ingestErr := a.ingestOne(ctx, in, pushed{udid: udid, stamp: stamp, key: key, now: now}, base)
			if refused, ok := errors.AsType[refusedPush](ingestErr); ok {
				r = Ingested{UDID: udid, Stamp: stamp, Err: refused.err}
			} else if ingestErr != nil {
				return out, ingestErr
			}
			if r.Stamp != "" {
				out = append(out, r)
			}
			if rmErr := removePush(in, udid, stamp); rmErr != nil {
				return out, rmErr
			}
		}
		b, err := base(udid)
		if err != nil {
			return out, err
		}
		if pubErr := a.publish(in, udid, b); pubErr != nil {
			return out, pubErr
		}
	}
	return out, nil
}

// refusedPush is why a push was refused: it is dropped, and the next push
// sends it again.
type refusedPush struct{ err error }

func (r refusedPush) Error() string { return r.err.Error() }

// pushed is one committed push and what checking it needs.
type pushed struct {
	udid, stamp string
	key         PushKey
	now         time.Time
}

// maxSignatureBytes bounds a signature file (64 hex digits and a newline).
const maxSignatureBytes = 1 << 10

// ingestOne takes one push in. A push that cannot be taken in fails with a
// refusedPush; other errors are problems with the archive itself, and keep
// the push. A push the archive holds already returns a zero Ingested.
func (a *Archive) ingestOne(ctx context.Context, in *os.Root, p pushed, base func(string) (*Snapshot, error)) (Ingested, error) {
	udid, stamp := p.udid, p.stamp
	t, err := ParseStamp(stamp)
	if err != nil {
		return Ingested{}, refusedPush{err}
	}
	has, err := a.Has(udid, t)
	if err != nil || has {
		return Ingested{}, err
	}
	raw, err := readSmall(in, udid+"/"+stamp+manifestExt, maxManifestBytes)
	if err != nil {
		return Ingested{}, refusedPush{err}
	}
	sig, err := readSmall(in, udid+"/"+stamp+signatureExt, maxSignatureBytes)
	if err != nil {
		return Ingested{}, refusedPush{fmt.Errorf("the push is not signed: %w", err)}
	}
	if !p.key.verify(udid, stamp, raw, sig) {
		return Ingested{}, refusedPush{errors.New("the push is not signed with this archive's push key (homecloud iphone push-key)")}
	}
	m, err := ParseManifest(bytes.NewReader(raw))
	if err != nil {
		return Ingested{}, refusedPush{err}
	}
	b, err := base(udid)
	if err != nil {
		return Ingested{}, err
	}
	open := func(rel string) (io.ReadCloser, error) {
		f, openErr := in.Open(udid + "/" + stamp + "/" + rel)
		if openErr != nil {
			return nil, openErr //nolint:wrapcheck // wrapped by the copy
		}
		if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
			return nil, errors.Join(fmt.Errorf("%s is not a regular file", rel), statErr, f.Close())
		}
		return f, nil
	}
	check := func() error { return checkPushed(ctx, a.Path(udid+"/"+partialPrefix+stamp), udid, t, p.now) }
	snap, st, err := a.add(ctx, udid, t, m, b, open, check)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Ingested{}, fmt.Errorf("ingest cancelled: %w", ctxErr)
		}
		if platform.IsDiskFull(err) {
			return Ingested{}, errors.Join(fmt.Errorf("the archive volume is full; the push of %s is kept", stamp), err,
				a.root.RemoveAll(udid+"/"+partialPrefix+stamp))
		}
		// Clear what the refused push left in the archive.
		if rmErr := a.root.RemoveAll(udid + "/" + partialPrefix + stamp); rmErr != nil {
			return Ingested{}, fmt.Errorf("remove refused copy %s: %w", stamp, rmErr)
		}
		return Ingested{}, refusedPush{err}
	}
	return Ingested{UDID: udid, Stamp: stamp, Snapshot: snap, Stats: st}, nil
}

// checkPushed checks a pushed copy before it is kept: it must be what this
// computer's own backups must be to be copied (iphonebackup.Judge, its age
// aside: an old backup is still worth a copy), of the device it is filed
// under, and finished at the time it is filed under. The computer that
// pushes could otherwise file any backup anywhere.
func checkPushed(ctx context.Context, dir, udid string, t, now time.Time) error {
	b := iphonebackup.Inspect(ctx, dir, udid)
	if b.Err == nil && b.UDID != udid {
		return fmt.Errorf("the pushed backup is of device %s, not %s", b.UDID, udid)
	}
	v, _ := iphonebackup.Judge(b, 0, iphonebackup.Record{}, iphonebackup.Policy{MaxAge: math.MaxInt64}, now)
	if v.State != iphonebackup.StateOK {
		return fmt.Errorf("the pushed backup is not a healthy backup: %s", strings.Join(v.Reasons, "; "))
	}
	if got := b.CompletedAt.UTC().Truncate(time.Second); !got.Equal(t) {
		return fmt.Errorf("the pushed backup finished at %s, not at %s as it was filed", Stamp(got), Stamp(t))
	}
	return nil
}

// publish writes the manifest of b (the snapshot the next push is based on)
// to the device's incoming folder, replacing the one before; nil removes it.
func (a *Archive) publish(in *os.Root, udid string, b *Snapshot) error {
	entries, err := fs.ReadDir(in.FS(), udid)
	if err != nil {
		return fmt.Errorf("list %s in %s: %w", udid, in.Name(), err)
	}
	want := ""
	if b != nil {
		want = archivedPrefix + b.Stamp + manifestExt
		if !slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() == want }) {
			m, err := a.Manifest(*b)
			if err != nil {
				return err
			}
			tmp := udid + "/." + want + ".tmp"
			if err := writeFile(in, tmp, m.Bytes()); err != nil {
				return err
			}
			if err := in.Rename(tmp, udid+"/"+want); err != nil {
				return fmt.Errorf("publish %s: %w", want, err)
			}
		}
	}
	for _, e := range entries {
		name := e.Name()
		if name != want && strings.HasPrefix(name, archivedPrefix) && strings.HasSuffix(name, manifestExt) {
			if err := in.Remove(udid + "/" + name); err != nil {
				return fmt.Errorf("remove %s/%s: %w", udid, name, err)
			}
		}
	}
	return nil
}
