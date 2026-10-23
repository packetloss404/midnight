// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	encryptedArchiveVersion    = 1
	encryptedArchiveChunkBytes = 64 << 10
	encryptedArchiveKDFRounds  = 310_000
	encryptedArchiveSaltBytes  = 16
	encryptedArchiveNonceBytes = 8
)

var encryptedArchiveMagic = [16]byte{'M', 'I', 'D', 'N', 'I', 'G', 'H', 'T', '-', 'B', 'A', 'C', 'K', 'U', 'P', 0}

// StreamBackupResult is evidence produced only after encrypted bytes have
// reached the configured Destination. SourceSHA256 hashes the uncompressed
// source stream; Object.SHA256 hashes the compressed encrypted object.
type StreamBackupResult struct {
	Object       Object `json:"object"`
	SourceBytes  int64  `json:"source_bytes"`
	SourceSHA256 string `json:"source_sha256"`
	Format       string `json:"format"`
}

// StreamRestoreResult is returned only after the complete encrypted envelope,
// gzip trailer, and optional expected object checksum have been verified.
// Callers should always restore into a replacement volume and publish the
// attachment swap only after receiving this result.
type StreamRestoreResult struct {
	Object         Object `json:"object"`
	RestoredBytes  int64  `json:"restored_bytes"`
	RestoredSHA256 string `json:"restored_sha256"`
	Format         string `json:"format"`
}

type streamProduceResult struct {
	bytes  int64
	sha256 string
	err    error
}

// PutEncryptedArchive compresses source with gzip and encrypts the compressed
// bytes in independently authenticated AES-256-GCM chunks. The passphrase is
// never written to the destination or returned in evidence.
func PutEncryptedArchive(ctx context.Context, destination Destination, key string, source io.Reader, passphrase []byte) (StreamBackupResult, error) {
	if err := ctx.Err(); err != nil {
		return StreamBackupResult{}, err
	}
	if destination == nil {
		return StreamBackupResult{}, errors.New("backup archive destination is required")
	}
	if source == nil {
		return StreamBackupResult{}, errors.New("backup archive source is required")
	}
	if len(passphrase) < 16 {
		return StreamBackupResult{}, errors.New("backup archive passphrase must be at least 16 bytes")
	}
	if strings.TrimSpace(key) == "" {
		return StreamBackupResult{}, errors.New("backup archive object key is required")
	}

	reader, writer := io.Pipe()
	produced := make(chan streamProduceResult, 1)
	go func() {
		result := streamEncryptedArchive(ctx, writer, source, passphrase)
		_ = writer.CloseWithError(result.err)
		produced <- result
	}()

	object, putErr := destination.Put(ctx, key, reader)
	if putErr != nil {
		_ = reader.CloseWithError(putErr)
	}
	produceResult := <-produced
	_ = reader.Close()
	if putErr != nil {
		return StreamBackupResult{}, fmt.Errorf("backup archive store: %w", putErr)
	}
	if produceResult.err != nil {
		return StreamBackupResult{}, produceResult.err
	}
	if object.Bytes <= 0 || strings.TrimSpace(object.SHA256) == "" {
		return StreamBackupResult{}, errors.New("backup archive destination did not return byte and checksum evidence")
	}
	return StreamBackupResult{
		Object:       object,
		SourceBytes:  produceResult.bytes,
		SourceSHA256: produceResult.sha256,
		Format:       "midnight-backup-aes256gcm-gzip-v1",
	}, nil
}

func streamEncryptedArchive(ctx context.Context, out io.Writer, source io.Reader, passphrase []byte) streamProduceResult {
	encrypted, err := newChunkEncryptWriter(out, passphrase)
	if err != nil {
		return streamProduceResult{err: fmt.Errorf("backup archive encrypt: %w", err)}
	}
	compressed := gzip.NewWriter(encrypted)
	hash := sha256.New()
	written, copyErr := io.Copy(compressed, io.TeeReader(&contextReader{ctx: ctx, reader: source}, hash))
	gzipErr := compressed.Close()
	encryptErr := encrypted.Close()
	err = errors.Join(copyErr, gzipErr, encryptErr)
	if err != nil {
		return streamProduceResult{err: fmt.Errorf("backup archive stream: %w", err)}
	}
	return streamProduceResult{
		bytes:  written,
		sha256: hex.EncodeToString(hash.Sum(nil)),
	}
}

// GetEncryptedArchive authenticates, decrypts, and decompresses one stored
// archive into target. expectedObjectSHA256 must be the checksum returned by
// PutEncryptedArchive; requiring it prevents a mutable destination from
// silently substituting another valid encrypted object.
func GetEncryptedArchive(ctx context.Context, destination Destination, key string, target io.Writer, passphrase []byte, expectedObjectSHA256 string) (StreamRestoreResult, error) {
	if err := ctx.Err(); err != nil {
		return StreamRestoreResult{}, err
	}
	if destination == nil {
		return StreamRestoreResult{}, errors.New("restore archive destination is required")
	}
	if target == nil {
		return StreamRestoreResult{}, errors.New("restore archive target is required")
	}
	if len(passphrase) < 16 {
		return StreamRestoreResult{}, errors.New("restore archive passphrase must be at least 16 bytes")
	}
	expectedObjectSHA256 = strings.ToLower(strings.TrimSpace(expectedObjectSHA256))
	if len(expectedObjectSHA256) != sha256.Size*2 {
		return StreamRestoreResult{}, errors.New("restore archive expected object sha256 is required")
	}

	body, object, err := destination.Get(ctx, key)
	if err != nil {
		return StreamRestoreResult{}, fmt.Errorf("restore archive open: %w", err)
	}
	defer func() { _ = body.Close() }()
	archiveHash := sha256.New()
	encrypted, err := newChunkDecryptReader(io.TeeReader(&contextReader{ctx: ctx, reader: body}, archiveHash), passphrase)
	if err != nil {
		return StreamRestoreResult{}, fmt.Errorf("restore archive decrypt: %w", err)
	}
	compressed, err := gzip.NewReader(encrypted)
	if err != nil {
		return StreamRestoreResult{}, fmt.Errorf("restore archive gzip: %w", err)
	}
	sourceHash := sha256.New()
	restored, copyErr := io.Copy(io.MultiWriter(target, sourceHash), compressed)
	closeErr := compressed.Close()
	// gzip is allowed to stop after its authenticated trailer while the
	// envelope footer is still buffered. Drain the decryptor so truncation or
	// an unauthenticated early end cannot be accepted as a successful restore.
	_, envelopeErr := io.Copy(io.Discard, encrypted)
	if err := errors.Join(copyErr, closeErr, envelopeErr); err != nil {
		return StreamRestoreResult{}, fmt.Errorf("restore archive stream: %w", err)
	}
	observedObjectSHA := hex.EncodeToString(archiveHash.Sum(nil))
	if observedObjectSHA != expectedObjectSHA256 {
		return StreamRestoreResult{}, fmt.Errorf("restore archive object checksum mismatch: got %s", observedObjectSHA)
	}
	object.SHA256 = observedObjectSHA
	return StreamRestoreResult{
		Object:         object,
		RestoredBytes:  restored,
		RestoredSHA256: hex.EncodeToString(sourceHash.Sum(nil)),
		Format:         "midnight-backup-aes256gcm-gzip-v1",
	}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type chunkEncryptWriter struct {
	out       io.Writer
	aead      cipher.AEAD
	header    []byte
	nonceBase [encryptedArchiveNonceBytes]byte
	buffer    []byte
	sequence  uint32
	closed    bool
}

func newChunkEncryptWriter(out io.Writer, passphrase []byte) (*chunkEncryptWriter, error) {
	if out == nil {
		return nil, errors.New("encrypted archive writer is required")
	}
	var salt [encryptedArchiveSaltBytes]byte
	var nonceBase [encryptedArchiveNonceBytes]byte
	if _, err := io.ReadFull(rand.Reader, salt[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, nonceBase[:]); err != nil {
		return nil, err
	}
	key := pbkdf2.Key(passphrase, salt[:], encryptedArchiveKDFRounds, 32, sha256.New)
	defer zeroBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, len(encryptedArchiveMagic)+1+encryptedArchiveSaltBytes+encryptedArchiveNonceBytes+8)
	header = append(header, encryptedArchiveMagic[:]...)
	header = append(header, byte(encryptedArchiveVersion))
	header = append(header, salt[:]...)
	header = append(header, nonceBase[:]...)
	var parameters [8]byte
	binary.BigEndian.PutUint32(parameters[0:4], encryptedArchiveKDFRounds)
	binary.BigEndian.PutUint32(parameters[4:8], encryptedArchiveChunkBytes)
	header = append(header, parameters[:]...)
	if _, err := out.Write(header); err != nil {
		return nil, err
	}
	return &chunkEncryptWriter{
		out:       out,
		aead:      aead,
		header:    header,
		nonceBase: nonceBase,
		buffer:    make([]byte, 0, encryptedArchiveChunkBytes),
	}, nil
}

func (w *chunkEncryptWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("encrypted archive writer is closed")
	}
	accepted := 0
	for len(p) > 0 {
		space := encryptedArchiveChunkBytes - len(w.buffer)
		take := len(p)
		if take > space {
			take = space
		}
		w.buffer = append(w.buffer, p[:take]...)
		p = p[take:]
		accepted += take
		if len(w.buffer) == encryptedArchiveChunkBytes {
			if err := w.flush(false); err != nil {
				return accepted, err
			}
		}
	}
	return accepted, nil
}

func (w *chunkEncryptWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if len(w.buffer) > 0 {
		if err := w.flush(false); err != nil {
			return err
		}
	}
	return w.flush(true)
}

func (w *chunkEncryptWriter) flush(final bool) error {
	plain := w.buffer
	if final {
		plain = nil
	}
	length := uint32(len(plain))
	var frameHeader [8]byte
	binary.BigEndian.PutUint32(frameHeader[0:4], length)
	binary.BigEndian.PutUint32(frameHeader[4:8], w.sequence)
	nonce := makeArchiveNonce(w.nonceBase, w.sequence)
	additional := append(append([]byte(nil), w.header...), frameHeader[:]...)
	sealed := w.aead.Seal(nil, nonce, plain, additional)
	if _, err := w.out.Write(frameHeader[:4]); err != nil {
		return err
	}
	if _, err := w.out.Write(sealed); err != nil {
		return err
	}
	w.sequence++
	w.buffer = w.buffer[:0]
	return nil
}

type chunkDecryptReader struct {
	in         io.Reader
	aead       cipher.AEAD
	header     []byte
	nonceBase  [encryptedArchiveNonceBytes]byte
	chunkBytes uint32
	sequence   uint32
	plain      []byte
	final      bool
}

func newChunkDecryptReader(in io.Reader, passphrase []byte) (*chunkDecryptReader, error) {
	if in == nil {
		return nil, errors.New("encrypted archive reader is required")
	}
	headerLength := len(encryptedArchiveMagic) + 1 + encryptedArchiveSaltBytes + encryptedArchiveNonceBytes + 8
	header := make([]byte, headerLength)
	if _, err := io.ReadFull(in, header); err != nil {
		return nil, err
	}
	if string(header[:len(encryptedArchiveMagic)]) != string(encryptedArchiveMagic[:]) {
		return nil, errors.New("encrypted archive magic is invalid")
	}
	offset := len(encryptedArchiveMagic)
	if int(header[offset]) != encryptedArchiveVersion {
		return nil, errors.New("encrypted archive version is unsupported")
	}
	offset++
	salt := header[offset : offset+encryptedArchiveSaltBytes]
	offset += encryptedArchiveSaltBytes
	var nonceBase [encryptedArchiveNonceBytes]byte
	copy(nonceBase[:], header[offset:offset+encryptedArchiveNonceBytes])
	offset += encryptedArchiveNonceBytes
	rounds := binary.BigEndian.Uint32(header[offset : offset+4])
	chunkBytes := binary.BigEndian.Uint32(header[offset+4 : offset+8])
	if rounds != encryptedArchiveKDFRounds || chunkBytes == 0 || chunkBytes > 4*encryptedArchiveChunkBytes {
		return nil, errors.New("encrypted archive parameters are invalid")
	}
	key := pbkdf2.Key(passphrase, salt, int(rounds), 32, sha256.New)
	defer zeroBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &chunkDecryptReader{
		in:         in,
		aead:       aead,
		header:     header,
		nonceBase:  nonceBase,
		chunkBytes: chunkBytes,
	}, nil
}

func (r *chunkDecryptReader) Read(p []byte) (int, error) {
	if len(r.plain) == 0 && !r.final {
		if err := r.readFrame(); err != nil {
			return 0, err
		}
	}
	if len(r.plain) == 0 && r.final {
		return 0, io.EOF
	}
	n := copy(p, r.plain)
	r.plain = r.plain[n:]
	return n, nil
}

func (r *chunkDecryptReader) readFrame() error {
	var lengthBytes [4]byte
	if _, err := io.ReadFull(r.in, lengthBytes[:]); err != nil {
		return fmt.Errorf("encrypted archive footer missing: %w", err)
	}
	length := binary.BigEndian.Uint32(lengthBytes[:])
	if length > r.chunkBytes {
		return errors.New("encrypted archive frame exceeds declared chunk size")
	}
	sealed := make([]byte, int(length)+r.aead.Overhead())
	if _, err := io.ReadFull(r.in, sealed); err != nil {
		return fmt.Errorf("encrypted archive frame truncated: %w", err)
	}
	var frameHeader [8]byte
	copy(frameHeader[0:4], lengthBytes[:])
	binary.BigEndian.PutUint32(frameHeader[4:8], r.sequence)
	nonce := makeArchiveNonce(r.nonceBase, r.sequence)
	additional := append(append([]byte(nil), r.header...), frameHeader[:]...)
	plain, err := r.aead.Open(nil, nonce, sealed, additional)
	if err != nil {
		return errors.New("encrypted archive authentication failed")
	}
	r.sequence++
	if length == 0 {
		r.final = true
		return nil
	}
	r.plain = plain
	return nil
}

func makeArchiveNonce(base [encryptedArchiveNonceBytes]byte, sequence uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, base[:])
	binary.BigEndian.PutUint32(nonce[8:], sequence)
	return nonce
}
