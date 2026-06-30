package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func (rs *RemoteSandbox) ReadFile(ctx context.Context, path, userID string) ([]byte, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(ReadFileRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoReadFile, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		return nil, parseSandboxErrorResponse(resp.Body, "read file")
	}
	var fc FileContentResponse
	if err := json.Unmarshal(resp.Body, &fc); err != nil {
		return nil, fmt.Errorf("unmarshal file content: %w", err)
	}
	return base64.StdEncoding.DecodeString(fc.Data)
}

func (rs *RemoteSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(WriteFileRequest{
		Path: path,
		Data: base64.StdEncoding.EncodeToString(data),
		Perm: int(perm),
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoWriteFile, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		return parseSandboxErrorResponse(resp.Body, "write file")
	}
	return nil
}

func (rs *RemoteSandbox) Stat(ctx context.Context, path, userID string) (*SandboxFileInfo, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(StatRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoStat, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		return nil, parseSandboxErrorResponse(resp.Body, "stat")
	}
	var sr StatResponse
	if err := json.Unmarshal(resp.Body, &sr); err != nil {
		return nil, fmt.Errorf("unmarshal stat: %w", err)
	}
	modTime, _ := time.Parse(time.RFC3339, sr.ModTime)
	return &SandboxFileInfo{
		Name:    sr.Name,
		Size:    sr.Size,
		Mode:    os.FileMode(sr.Mode),
		ModTime: modTime,
		IsDir:   sr.IsDir,
	}, nil
}

func (rs *RemoteSandbox) ReadDir(ctx context.Context, path, userID string) ([]DirEntry, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(ReadDirRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoReadDir, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		return nil, parseSandboxErrorResponse(resp.Body, "read_dir")
	}
	var de DirEntriesResponse
	if err := json.Unmarshal(resp.Body, &de); err != nil {
		return nil, fmt.Errorf("unmarshal dir entries: %w", err)
	}
	entries := make([]DirEntry, len(de.Entries))
	for i, e := range de.Entries {
		entries[i] = DirEntry{Name: e.Name, IsDir: e.IsDir, Size: e.Size} //nolint:staticcheck
	}
	return entries, nil
}

func (rs *RemoteSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(PathRequest{Path: path, Perm: int(perm)})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoMkdirAll, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		return parseSandboxErrorResponse(resp.Body, "mkdir_all")
	}
	return nil
}

func (rs *RemoteSandbox) Remove(ctx context.Context, path, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(PathRequest{Path: path})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoRemove, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		return parseSandboxErrorResponse(resp.Body, "remove")
	}
	return nil
}

func (rs *RemoteSandbox) RemoveAll(ctx context.Context, path, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(PathRequest{Path: path})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoRemoveAll, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		return parseSandboxErrorResponse(resp.Body, "remove_all")
	}
	return nil
}

func (rs *RemoteSandbox) DownloadFile(ctx context.Context, url, outputPath, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(DownloadFileRequest{
		URL:        url,
		OutputPath: outputPath,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoDownloadFile, UserID: userID, Body: reqBody}
	// 5-minute timeout for downloads
	resp, err := rs.sendRequest(ctx, rc, msg, 5*time.Minute)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		return parseSandboxErrorResponse(resp.Body, "download_file")
	}
	return nil
}
