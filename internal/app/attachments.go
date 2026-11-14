package app

import (
	"bytes"
	"context"
	"path/filepath"

	"recall/internal/attachment"
	"recall/internal/document"
	"recall/internal/model"
)

const maxNameAttempts = 999

// Attachment is a stored image and the Markdown destination that links to it
// from its note's folder.
type Attachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Link string `json:"link"`
}

// SaveAttachment stores an image in the note's attachment folder, creating
// the folder if needed. The format comes from the bytes, the name is
// sanitised, and an existing file is never overwritten: clashes get -2, -3, ...
func (a *App) SaveAttachment(ctx context.Context, ref model.Ref, name string, data []byte) (Attachment, error) {
	if len(data) > attachment.MaxBytes {
		return Attachment{}, appError("invalid_argument", "", "images must be 20 MB or smaller")
	}
	format, ok := attachment.Detect(data)
	if !ok {
		return Attachment{}, appError("invalid_argument", "", "only PNG, JPEG, GIF and WebP images are supported")
	}
	var saved Attachment
	_, err := a.mutateDocument(ctx, ref, "", func(note model.Document) (model.Document, error) {
		folder := attachment.Folder(note.Path)
		// Mkdir refuses a symlink (outside_root). An existing regular file
		// reports already_exists here and is refused by Write's no-follow
		// directory open below (invalid_argument).
		if err := a.store.Mkdir(ctx, folder); err != nil && model.ErrorCode(err) != "already_exists" {
			return model.Document{}, err
		}
		for n := 1; n <= maxNameAttempts; n++ {
			file := attachment.FileName(name, format, n)
			path := filepath.Join(folder, file)
			err := a.store.Write(ctx, path, "", data)
			if model.ErrorCode(err) == "already_exists" {
				continue
			}
			if err != nil {
				return model.Document{}, err
			}
			saved = Attachment{Path: path, Name: file, Link: attachment.Destination(filepath.Join(filepath.Base(folder), file))}
			return note, nil
		}
		return model.Document{}, appError("already_exists", folder, "too many images share this name")
	})
	return saved, err
}

// AttachmentCount counts the files directly inside a note's attachment folder.
func (a *App) AttachmentCount(ctx context.Context, ref model.Ref) (int, error) {
	note, err := a.Read(ctx, ref)
	if err != nil {
		return 0, err
	}
	return a.countAttachments(ctx, attachment.Folder(note.Path))
}

func (a *App) countAttachments(ctx context.Context, folder string) (int, error) {
	entries, err := a.store.List(ctx, folder)
	if model.ErrorCode(err) == "not_found" {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir && !entry.IsSymlink {
			count++
		}
	}
	return count, nil
}

// ReadAttachment returns an image stored directly inside an attachment folder.
// It reads through the confined, no-follow store and requires the bytes to
// match an accepted format and the file's extension, so nothing but genuine
// images in .assets folders is ever returned.
func (a *App) ReadAttachment(ctx context.Context, path string) ([]byte, attachment.Format, error) {
	if err := validatePath(path); err != nil {
		return nil, attachment.Format{}, err
	}
	notAttachment := appError("not_found", path, "not a note image")
	if !attachment.IsFolderName(filepath.Base(filepath.Dir(path))) {
		return nil, attachment.Format{}, notAttachment
	}
	want, ok := attachment.ForExtension(filepath.Ext(path))
	if !ok {
		return nil, attachment.Format{}, notAttachment
	}
	data, err := a.store.Read(ctx, path)
	if err != nil {
		return nil, attachment.Format{}, err
	}
	if got, ok := attachment.Detect(data); !ok || got != want {
		return nil, attachment.Format{}, notAttachment
	}
	return data, want, nil
}

// removeAttachments deletes a deleted note's attachment folder. A missing
// folder or a regular file of that name is left alone; anything that cannot be
// removed (including a symlink, which is never followed) is a partial failure.
func (a *App) removeAttachments(ctx context.Context, notePath string) error {
	folder := attachment.Folder(notePath)
	entry, err := a.store.Stat(ctx, folder)
	if model.ErrorCode(err) == "not_found" {
		return nil
	}
	if err == nil && !entry.IsDir && !entry.IsSymlink {
		return nil
	}
	if err == nil && entry.IsSymlink {
		err = appError("outside_root", folder, "symlinks are not allowed")
	}
	if err == nil {
		err = a.store.Remove(ctx, folder, true)
	}
	if err == nil {
		return nil
	}
	return appError("partial_failure", folder, "note deleted, but its attached images could not be removed: "+model.PublicError(err).Message)
}

// attachmentsToCarry reports whether a note's attachment folder must move with
// it, refusing the move before anything changes when it cannot.
func (a *App) attachmentsToCarry(ctx context.Context, from, to string) (bool, error) {
	entry, err := a.store.Stat(ctx, from)
	if model.ErrorCode(err) == "not_found" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if entry.IsSymlink {
		return false, appError("outside_root", from, "symlinks are not allowed")
	}
	if !entry.IsDir {
		return false, nil
	}
	if _, err := a.store.Stat(ctx, to); err == nil {
		return false, appError("conflict", to, "the destination already has an attachments folder")
	} else if model.ErrorCode(err) != "not_found" {
		return false, err
	}
	return true, nil
}

// rewriteAttachmentLinks repoints body links from one attachment folder name
// to another, leaving frontmatter and all other text byte-for-byte intact.
func rewriteAttachmentLinks(path string, raw []byte, from, to string) ([]byte, bool, error) {
	if from == to {
		return raw, false, nil
	}
	p, err := document.Parse(path, raw)
	if err != nil {
		return nil, false, err
	}
	body := attachment.RewriteLinks(p.Body, from, to)
	if bytes.Equal(body, p.Body) {
		return raw, false, nil
	}
	p.Body = body
	out, err := document.Encode(p)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
