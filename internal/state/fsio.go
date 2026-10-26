package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	previousSuffix = ".previous"
	tempSuffix     = ".tmp"
	corruptSuffix  = ".corrupt"
)

// Fault-injection points passed to Options.Fault, in the order they occur
// during an atomic write. Returning an error from the hook aborts the write
// at that point and leaves the files exactly as a crash there would.
const (
	FaultTempWritten      = "temp-written"    // temp file fsynced, nothing renamed
	FaultPreviousLinked   = "previous-linked" // .previous refreshed, target untouched
	FaultRenamed          = "renamed"         // target replaced, directory not synced
	FaultDeletePrevious   = "delete-previous" // .previous removed, target still present
	FaultDeleteTargetGone = "delete-target"   // target removed, directory not synced
)

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse fsync on directories; that is not data loss
		// we can act on, so only EINVAL-like errors are tolerated.
		if errors.Is(err, os.ErrInvalid) {
			return nil
		}
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// atomicWrite replaces path with data: temp file, fsync, keep the old version
// as path+".previous", rename, fsync of the directory.
func atomicWrite(path string, data []byte, fault func(string) error) error {
	dir := filepath.Dir(path)
	tmp := path + tempSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := hit(fault, FaultTempWritten); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		// Refresh .previous atomically: link to a scratch name, then rename over it.
		prev := path + previousSuffix
		scratch := prev + tempSuffix
		os.Remove(scratch)
		if err := os.Link(path, scratch); err != nil {
			if err := copyFile(path, scratch); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("keep previous: %w", err)
			}
		}
		if err := os.Rename(scratch, prev); err != nil {
			os.Remove(scratch)
			os.Remove(tmp)
			return err
		}
		if err := hit(fault, FaultPreviousLinked); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := hit(fault, FaultRenamed); err != nil {
		return err
	}
	return syncDir(dir)
}

func hit(fault func(string) error, point string) error {
	if fault == nil {
		return nil
	}
	return fault(point)
}

func removeObjectFiles(path string, fault func(string) error) error {
	if err := os.Remove(path + previousSuffix); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := hit(fault, FaultDeletePrevious); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := hit(fault, FaultDeleteTargetGone); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func encodeObject(o *Object) ([]byte, error) {
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// decodeObject parses, migrates and validates one document. The second
// result reports that a migration changed it.
func decodeObject(data []byte, migs []Migration) (*Object, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, false, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return nil, false, errors.New("trailing data after document")
	}
	changed, err := migrate(doc, migs)
	if err != nil {
		return nil, false, err
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, false, err
	}
	var o Object
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, false, fmt.Errorf("decode object: %w", err)
	}
	if o.Kind == "" || o.Metadata.UUID == "" || o.Metadata.Name == "" {
		return nil, false, errors.New("document lacks kind, uuid or name")
	}
	if len(o.Spec) == 0 {
		return nil, false, errors.New("document lacks spec")
	}
	return &o, changed, nil
}
