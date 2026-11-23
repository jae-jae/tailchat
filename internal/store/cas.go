package store

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"lukechampine.com/blake3"

	"tailchat/internal/model"
)

var _ model.CASStore = (*FileCAS)(nil)

const maxThumbnailSourceBytes = 50 * 1024 * 1024 // 50 MiB safety cap for thumbnail decoding

// FileCAS implements model.CASStore on the local filesystem using BLAKE3 content addressing.
type FileCAS struct {
	rootDir string
	outMu   sync.Mutex // serializes collision-free output filename allocation
}

// NewCAS initializes a FileCAS rooted at rootDir.
func NewCAS(rootDir string) (*FileCAS, error) {
	if rootDir == "" {
		return nil, errors.New("rootDir must not be empty")
	}
	for _, sub := range []string{"chunks", "blobs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create cas dir %s: %w", sub, err)
		}
	}
	return &FileCAS{rootDir: rootDir}, nil
}

func (c *FileCAS) chunkDir(blobHash string) string {
	h := strings.ToLower(blobHash)
	return filepath.Join(c.rootDir, "chunks", h[:2], h)
}

func (c *FileCAS) chunkPath(blobHash string, index int) string {
	return filepath.Join(c.chunkDir(blobHash), strconv.Itoa(index)+".chk")
}

func (c *FileCAS) blobPath(blobHash string) string {
	h := strings.ToLower(blobHash)
	return filepath.Join(c.rootDir, "blobs", h[:2], h)
}

// IngestFile stream-reads srcPath in 1 MiB chunks, computes per-chunk and whole-file BLAKE3 hashes,
// persists both chunks and the complete blob into CAS, and generates an optional <=32KB PNG thumbnail.
func (c *FileCAS) IngestFile(srcPath string) (*model.FileBlob, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat source file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("cannot ingest directory %q", srcPath)
	}

	stageDir, err := os.MkdirTemp(filepath.Join(c.rootDir, "tmp"), "ingest-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()

	stagedBlobPath := filepath.Join(stageDir, "complete.blob")
	stagedBlob, err := os.Create(stagedBlobPath)
	if err != nil {
		return nil, fmt.Errorf("create staged blob: %w", err)
	}
	defer func() {
		if stagedBlob != nil {
			_ = stagedBlob.Close()
		}
	}()

	rootHasher := blake3.New(32, nil)
	buf := make([]byte, model.DefaultChunkSize)
	chunkHashes := make([]string, 0)
	var (
		totalSize int64
		sniffBuf  []byte
		chunkIdx  int
	)

	for {
		n, readErr := io.ReadFull(src, buf)
		if n > 0 {
			chunk := buf[:n]
			if len(sniffBuf) < 512 {
				need := 512 - len(sniffBuf)
				if n < need {
					need = n
				}
				sniffBuf = append(sniffBuf, chunk[:need]...)
			}

			if _, err := rootHasher.Write(chunk); err != nil {
				return nil, fmt.Errorf("hash root chunk %d: %w", chunkIdx, err)
			}
			if _, err := stagedBlob.Write(chunk); err != nil {
				return nil, fmt.Errorf("write staged blob chunk %d: %w", chunkIdx, err)
			}

			chunkHashes = append(chunkHashes, model.Blake3Hex(chunk))
			stagedChunkPath := filepath.Join(stageDir, strconv.Itoa(chunkIdx)+".chk")
			if err := os.WriteFile(stagedChunkPath, chunk, 0o644); err != nil {
				return nil, fmt.Errorf("write staged chunk %d: %w", chunkIdx, err)
			}
			totalSize += int64(n)
			chunkIdx++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read source file: %w", readErr)
		}
	}

	if err := stagedBlob.Close(); err != nil {
		return nil, fmt.Errorf("close staged blob: %w", err)
	}
	stagedBlob = nil

	blobHash := hex.EncodeToString(rootHasher.Sum(nil))
	finalBlobPath := c.blobPath(blobHash)
	if err := os.MkdirAll(filepath.Dir(finalBlobPath), 0o755); err != nil {
		return nil, fmt.Errorf("create blob dir: %w", err)
	}
	if err := os.Rename(stagedBlobPath, finalBlobPath); err != nil {
		return nil, fmt.Errorf("commit complete blob: %w", err)
	}

	if len(chunkHashes) > 0 {
		finalChunkDir := c.chunkDir(blobHash)
		if err := os.MkdirAll(finalChunkDir, 0o755); err != nil {
			return nil, fmt.Errorf("create chunk dir: %w", err)
		}
		for i := range chunkHashes {
			srcChunk := filepath.Join(stageDir, strconv.Itoa(i)+".chk")
			dstChunk := c.chunkPath(blobHash, i)
			if err := os.Rename(srcChunk, dstChunk); err != nil {
				return nil, fmt.Errorf("commit chunk %d: %w", i, err)
			}
		}
	}

	fileName := filepath.Base(srcPath)
	mimeType := detectMimeType(fileName, sniffBuf)

	var thumb []byte
	if totalSize > 0 && totalSize <= maxThumbnailSourceBytes && isThumbnailImageMIME(mimeType) {
		if f, err := os.Open(finalBlobPath); err == nil {
			thumb, _ = generatePNGThumbnail(f)
			_ = f.Close()
		}
	}

	return &model.FileBlob{
		BlobHash:      blobHash,
		FileName:      fileName,
		FileSize:      totalSize,
		MimeType:      mimeType,
		ChunkSize:     model.DefaultChunkSize,
		ChunkCount:    len(chunkHashes),
		ChunkHashes:   chunkHashes,
		ThumbnailWebP: thumb,
		LocalPath:     finalBlobPath,
	}, nil
}

// IngestBytes persists in-memory bytes as a content-addressed blob and its 1 MiB chunks.
func (c *FileCAS) IngestBytes(fileName, mimeType string, data []byte) (*model.FileBlob, error) {
	if strings.TrimSpace(fileName) == "" {
		fileName = "unnamed"
	} else {
		fileName = path.Base(strings.ReplaceAll(fileName, "\\", "/"))
	}

	if strings.TrimSpace(mimeType) == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = detectMimeType(fileName, sniff)
	}

	rootHasher := blake3.New(32, nil)
	_, _ = rootHasher.Write(data)
	blobHash := hex.EncodeToString(rootHasher.Sum(nil))

	finalBlobPath := c.blobPath(blobHash)
	if err := atomicWriteFile(finalBlobPath, data); err != nil {
		return nil, fmt.Errorf("write complete blob: %w", err)
	}

	chunkHashes := make([]string, 0)
	for offset, idx := 0, 0; offset < len(data); offset, idx = offset+model.DefaultChunkSize, idx+1 {
		end := offset + model.DefaultChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		chunkHashes = append(chunkHashes, model.Blake3Hex(chunk))
		if err := atomicWriteFile(c.chunkPath(blobHash, idx), chunk); err != nil {
			return nil, fmt.Errorf("write chunk %d: %w", idx, err)
		}
	}

	var thumb []byte
	if len(data) > 0 && len(data) <= maxThumbnailSourceBytes && isThumbnailImageMIME(mimeType) {
		thumb, _ = generatePNGThumbnail(bytes.NewReader(data))
	}

	return &model.FileBlob{
		BlobHash:      blobHash,
		FileName:      fileName,
		FileSize:      int64(len(data)),
		MimeType:      mimeType,
		ChunkSize:     model.DefaultChunkSize,
		ChunkCount:    len(chunkHashes),
		ChunkHashes:   chunkHashes,
		ThumbnailWebP: thumb,
		LocalPath:     finalBlobPath,
	}, nil
}

// ReadChunk returns chunk index i for blobHash.
func (c *FileCAS) ReadChunk(blobHash string, index int) ([]byte, error) {
	if err := validateBlobHash(blobHash); err != nil {
		return nil, err
	}
	if index < 0 {
		return nil, fmt.Errorf("invalid chunk index %d", index)
	}

	data, err := os.ReadFile(c.chunkPath(blobHash, index))
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Fallback: slice from the complete blob if it exists on disk.
	blobFile, openErr := os.Open(c.blobPath(blobHash))
	if openErr != nil {
		return nil, err
	}
	defer blobFile.Close()

	offset := int64(index) * int64(model.DefaultChunkSize)
	buf := make([]byte, model.DefaultChunkSize)
	n, readErr := blobFile.ReadAt(buf, offset)
	if n > 0 && (readErr == nil || errors.Is(readErr, io.EOF)) {
		return buf[:n], nil
	}
	if errors.Is(readErr, io.EOF) {
		return nil, os.ErrNotExist
	}
	return nil, readErr
}

// WriteChunk verifies chunk data against expectedHash before atomically writing it to CAS chunk storage.
func (c *FileCAS) WriteChunk(blobHash string, index int, expectedHash string, data []byte) error {
	if err := validateBlobHash(blobHash); err != nil {
		return err
	}
	if index < 0 {
		return fmt.Errorf("invalid chunk index %d", index)
	}
	actualHash := model.Blake3Hex(data)
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("chunk %d hash mismatch: got %s, want %s", index, actualHash, expectedHash)
	}
	return atomicWriteFile(c.chunkPath(blobHash, index), data)
}

// AssembleFile concatenates chunks 0..blob.ChunkCount-1, verifies whole-file BLAKE3 against blob.BlobHash,
// atomically persists the complete blob into CAS, and optionally writes a sanitized copy in outputDir.
func (c *FileCAS) AssembleFile(blob model.FileBlob, outputDir string) (string, error) {
	if err := validateBlobHash(blob.BlobHash); err != nil {
		return "", err
	}
	if blob.ChunkCount < 0 {
		return "", fmt.Errorf("invalid chunk count %d", blob.ChunkCount)
	}

	finalBlobPath := c.blobPath(blob.BlobHash)
	if c.verifyExistingBlob(finalBlobPath, blob.BlobHash, blob.FileSize) {
		if strings.TrimSpace(outputDir) == "" {
			return finalBlobPath, nil
		}
		return c.writeFriendlyCopy(finalBlobPath, blob.FileName, blob.BlobHash, outputDir)
	}

	if err := os.MkdirAll(filepath.Dir(finalBlobPath), 0o755); err != nil {
		return "", fmt.Errorf("create blob dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(finalBlobPath), ".assemble-*")
	if err != nil {
		return "", fmt.Errorf("create temp assemble file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	rootHasher := blake3.New(32, nil)
	var totalWritten int64

	for i := 0; i < blob.ChunkCount; i++ {
		chunkData, err := c.ReadChunk(blob.BlobHash, i)
		if err != nil {
			return "", fmt.Errorf("read chunk %d: %w", i, err)
		}
		if i < len(blob.ChunkHashes) && blob.ChunkHashes[i] != "" {
			if actual := model.Blake3Hex(chunkData); !strings.EqualFold(actual, blob.ChunkHashes[i]) {
				return "", fmt.Errorf("chunk %d integrity check failed: got %s, want %s", i, actual, blob.ChunkHashes[i])
			}
		}
		if _, err := rootHasher.Write(chunkData); err != nil {
			return "", fmt.Errorf("hash chunk %d: %w", i, err)
		}
		n, err := tmpFile.Write(chunkData)
		if err != nil {
			return "", fmt.Errorf("write chunk %d: %w", i, err)
		}
		totalWritten += int64(n)
	}

	actualRoot := hex.EncodeToString(rootHasher.Sum(nil))
	if !strings.EqualFold(actualRoot, blob.BlobHash) {
		return "", fmt.Errorf("assembled root hash mismatch: got %s, want %s", actualRoot, blob.BlobHash)
	}
	if blob.FileSize > 0 && totalWritten != blob.FileSize {
		return "", fmt.Errorf("assembled size mismatch: got %d, want %d", totalWritten, blob.FileSize)
	}

	if err := tmpFile.Sync(); err != nil {
		return "", fmt.Errorf("sync assembled file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close assembled file: %w", err)
	}
	if err := os.Rename(tmpPath, finalBlobPath); err != nil {
		return "", fmt.Errorf("commit assembled blob: %w", err)
	}
	cleanupTmp = false

	if strings.TrimSpace(outputDir) == "" {
		return finalBlobPath, nil
	}

	outPath, err := c.writeFriendlyCopy(finalBlobPath, blob.FileName, blob.BlobHash, outputDir)
	if err != nil {
		return "", err
	}
	return outPath, nil
}

// HasCompleteBlob reports whether the complete file for blobHash exists in CAS.
func (c *FileCAS) HasCompleteBlob(blobHash string) (string, bool) {
	if err := validateBlobHash(blobHash); err != nil {
		return "", false
	}
	p := c.blobPath(blobHash)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

func (c *FileCAS) verifyExistingBlob(blobPath, expectedHash string, expectedSize int64) bool {
	f, err := os.Open(blobPath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return false
	}
	h := blake3.New(32, nil)
	n, err := io.Copy(h, f)
	if err != nil {
		return false
	}
	if expectedSize > 0 && n != expectedSize {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expectedHash)
}

func (c *FileCAS) writeFriendlyCopy(srcBlobPath, rawFileName, blobHash, outputDir string) (string, error) {
	cleanOutDir := filepath.Clean(outputDir)
	if err := os.MkdirAll(cleanOutDir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	safeName := sanitizeFileName(rawFileName, blobHash)

	// Stage full copy in outputDir first so committing the final path is atomic.
	tmpOut, err := os.CreateTemp(cleanOutDir, ".tailchat-out-*")
	if err != nil {
		return "", fmt.Errorf("create output temp file: %w", err)
	}
	tmpOutPath := tmpOut.Name()
	defer func() { _ = os.Remove(tmpOutPath) }()

	src, err := os.Open(srcBlobPath)
	if err != nil {
		_ = tmpOut.Close()
		return "", fmt.Errorf("open complete blob: %w", err)
	}
	if _, err := io.Copy(tmpOut, src); err != nil {
		_ = src.Close()
		_ = tmpOut.Close()
		return "", fmt.Errorf("copy to output temp file: %w", err)
	}
	_ = src.Close()
	if err := tmpOut.Sync(); err != nil {
		_ = tmpOut.Close()
		return "", fmt.Errorf("sync output temp file: %w", err)
	}
	if err := tmpOut.Close(); err != nil {
		return "", fmt.Errorf("close output temp file: %w", err)
	}

	c.outMu.Lock()
	defer c.outMu.Unlock()

	ext := filepath.Ext(safeName)
	stem := strings.TrimSuffix(safeName, ext)

	for i := 0; ; i++ {
		candidateName := safeName
		if i > 0 {
			candidateName = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		candidatePath := filepath.Join(cleanOutDir, candidateName)
		// Defense-in-depth containment check against path traversal.
		if filepath.Dir(candidatePath) != cleanOutDir {
			return "", fmt.Errorf("unsafe output path for filename %q", rawFileName)
		}

		// Use os.Link for race-free, non-clobbering atomic placement.
		if err := os.Link(tmpOutPath, candidatePath); err == nil {
			return candidatePath, nil
		} else if errors.Is(err, os.ErrExist) {
			continue
		} else {
			// Fallback on filesystems without hard-link support.
			if _, statErr := os.Stat(candidatePath); statErr == nil {
				continue
			}
			if renameErr := os.Rename(tmpOutPath, candidatePath); renameErr != nil {
				return "", fmt.Errorf("commit output file: %w", renameErr)
			}
			return candidatePath, nil
		}
	}
}

// sanitizeFileName strips directory traversal segments and unsafe characters from a filename.
func sanitizeFileName(rawName, fallbackHash string) string {
	// Normalize both POSIX and Windows path separators before taking base name.
	normalized := strings.ReplaceAll(rawName, "\\", "/")
	base := path.Base(normalized)

	var b strings.Builder
	b.Grow(len(base))
	for _, r := range base {
		switch {
		case r < 0x20 || r == 0x7f:
			// Strip control characters and NUL bytes.
			continue
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}

	cleaned := strings.TrimSpace(b.String())
	cleaned = strings.TrimLeft(cleaned, ".")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" || cleaned == "." || cleaned == ".." {
		prefix := fallbackHash
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		if prefix == "" {
			return "file"
		}
		return "file-" + prefix
	}

	// Cap overly long filenames while keeping the extension intact.
	const maxNameBytes = 200
	if len(cleaned) > maxNameBytes {
		ext := filepath.Ext(cleaned)
		if len(ext) > 32 {
			ext = ""
		}
		stemMax := maxNameBytes - len(ext)
		stem := strings.TrimSuffix(cleaned, ext)
		if len(stem) > stemMax {
			stem = stem[:stemMax]
		}
		cleaned = stem + ext
	}
	return cleaned
}

func validateBlobHash(h string) error {
	if len(h) != 64 {
		return fmt.Errorf("invalid blob hash length: %d", len(h))
	}
	for i := 0; i < len(h); i++ {
		ch := h[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return fmt.Errorf("invalid blob hash character at %d", i)
		}
	}
	return nil
}

func atomicWriteFile(dstPath string, data []byte) error {
	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func detectMimeType(fileName string, sniff []byte) string {
	if ext := filepath.Ext(fileName); ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			if parsed, _, err := mime.ParseMediaType(mt); err == nil && parsed != "" {
				return parsed
			}
			return mt
		}
	}
	if len(sniff) > 0 {
		mt := http.DetectContentType(sniff)
		if parsed, _, err := mime.ParseMediaType(mt); err == nil && parsed != "" {
			return parsed
		}
		return mt
	}
	return "application/octet-stream"
}

func isThumbnailImageMIME(mimeType string) bool {
	mt := mimeType
	if parsed, _, err := mime.ParseMediaType(mimeType); err == nil && parsed != "" {
		mt = parsed
	}
	switch strings.ToLower(mt) {
	case "image/png", "image/jpeg", "image/gif":
		return true
	default:
		return false
	}
}

// generatePNGThumbnail decodes an image and scales it down to a PNG <= model.MaxThumbnailBytes.
func generatePNGThumbnail(r io.Reader) ([]byte, error) {
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}

	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	dims := []int{256, 192, 128, 96, 64, 48, 32}

	for _, maxDim := range dims {
		scaled := resizeImageBox(src, maxDim)
		if scaled == nil {
			return nil, errors.New("empty image bounds")
		}
		var buf bytes.Buffer
		if err := encoder.Encode(&buf, scaled); err != nil {
			return nil, err
		}
		if buf.Len() <= model.MaxThumbnailBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, errors.New("thumbnail exceeded maximum byte budget")
}

// resizeImageBox downscales src to fit within maxDim x maxDim using box-area averaging.
func resizeImageBox(src image.Image, maxDim int) *image.NRGBA {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil
	}

	dstW, dstH := srcW, srcH
	if srcW > maxDim || srcH > maxDim {
		if srcW >= srcH {
			dstW = maxDim
			dstH = max(1, (srcH*maxDim)/srcW)
		} else {
			dstH = maxDim
			dstW = max(1, (srcW*maxDim)/srcH)
		}
	}

	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	scaleX := float64(srcW) / float64(dstW)
	scaleY := float64(srcH) / float64(dstH)

	for dy := 0; dy < dstH; dy++ {
		sy0 := int(float64(dy) * scaleY)
		sy1 := int(float64(dy+1) * scaleY)
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		if sy1 > srcH {
			sy1 = srcH
		}

		for dx := 0; dx < dstW; dx++ {
			sx0 := int(float64(dx) * scaleX)
			sx1 := int(float64(dx+1) * scaleX)
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			if sx1 > srcW {
				sx1 = srcW
			}

			var sumR, sumG, sumB, sumA, count uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					c := color.NRGBAModel.Convert(src.At(b.Min.X+sx, b.Min.Y+sy)).(color.NRGBA)
					sumR += uint64(c.R)
					sumG += uint64(c.G)
					sumB += uint64(c.B)
					sumA += uint64(c.A)
					count++
				}
			}
			if count == 0 {
				continue
			}
			dst.SetNRGBA(dx, dy, color.NRGBA{
				R: uint8(sumR / count),
				G: uint8(sumG / count),
				B: uint8(sumB / count),
				A: uint8(sumA / count),
			})
		}
	}
	return dst
}
