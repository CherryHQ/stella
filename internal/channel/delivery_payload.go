package channel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/agentrun"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/renderrefs"
)

type outputPartMetadata struct {
	Kind       string                 `json:"kind"`
	Name       string                 `json:"name,omitempty"`
	MimeType   string                 `json:"mime_type,omitempty"`
	References []renderrefs.Reference `json:"references,omitempty"`
}

// persistEvent captures rich output before forwarding it. Text already has a
// canonical transcript; images, files and reference cards need their own bytes
// or identifiers. Only the stream pump calls this method.
func (d *runDelivery) persistEvent(ctx context.Context, event pkgchannel.Event) (pkgchannel.Event, error) {
	if d == nil {
		return event, nil
	}
	if image := event.Image; image != nil {
		if err := d.persistParts(ctx, outputPartMetadata{Kind: "image", MimeType: image.MimeType},
			base64.NewDecoder(base64.StdEncoding, strings.NewReader(image.Data))); err != nil {
			return pkgchannel.Event{}, err
		}
	}
	if file := event.File; file != nil {
		source, err := os.Open(file.Path)
		if err != nil {
			return pkgchannel.Event{}, err
		}
		defer func() { _ = source.Close() }()
		snapshot, err := os.CreateTemp("", "stella-run-output-*")
		if err != nil {
			return pkgchannel.Event{}, err
		}
		defer func() { _ = snapshot.Close() }()
		name := file.Name
		if name == "" {
			name = filepath.Base(file.Path)
		}
		err = d.persistParts(ctx, outputPartMetadata{Kind: "file", Name: name}, io.TeeReader(source, snapshot))
		if closeErr := snapshot.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(snapshot.Name())
			return pkgchannel.Event{}, err
		}
		// A later workspace edit cannot change the file the adapter sends.
		event.File = &pkgchannel.FileEvent{Path: snapshot.Name(), Name: name}
		d.payloadMu.Lock()
		select {
		case <-d.done:
			_ = os.Remove(snapshot.Name())
		default:
			d.snapshots = append(d.snapshots, snapshot.Name())
		}
		d.payloadMu.Unlock()
	}
	if len(event.References) > 0 {
		if err := d.persistParts(ctx, outputPartMetadata{Kind: "references", References: event.References}, strings.NewReader("")); err != nil {
			return pkgchannel.Event{}, err
		}
	}
	return event, nil
}

func (d *runDelivery) persistParts(ctx context.Context, metadata outputPartMetadata, source io.Reader) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	d.nextEvent++
	// One MiB per chunk, independent of attachment size. Move the bytes to blob
	// storage if measured attachment traffic makes database storage expensive.
	buffer := make([]byte, 1<<20)
	for chunk := int32(0); ; chunk++ {
		n, readErr := io.ReadFull(source, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return fmt.Errorf("capture channel attachment: %w", readErr)
		}
		if n > 0 || chunk == 0 {
			if err := d.persistPart(ctx, d.nextEvent, chunk, encoded, buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil
		}
	}
}

func (d *runDelivery) persistPart(ctx context.Context, event int64, chunk int32, metadata, data []byte) error {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentrun.ValidateTx(d.lease.ContextWith(ctx), tx); err != nil {
		return err
	}
	if err := d.q.WithTx(tx).AppendAgentRunOutputPart(ctx, sqlc.AppendAgentRunOutputPartParams{
		RunID: d.runID, EventNo: event, ChunkNo: chunk, Metadata: metadata, Data: data,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
