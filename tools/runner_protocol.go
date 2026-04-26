package tools

import (
	"xbot/internal/runnerproto"
)

// Re-export all protocol types and constants from the shared package.
// This preserves backward compatibility for existing code that imports from tools.

// === WebSocket Protocol Constants ===

const (
	ProtoExec         = runnerproto.ProtoExec
	ProtoReadFile     = runnerproto.ProtoReadFile
	ProtoWriteFile    = runnerproto.ProtoWriteFile
	ProtoStat         = runnerproto.ProtoStat
	ProtoReadDir      = runnerproto.ProtoReadDir
	ProtoMkdirAll     = runnerproto.ProtoMkdirAll
	ProtoRemove       = runnerproto.ProtoRemove
	ProtoRemoveAll    = runnerproto.ProtoRemoveAll
	ProtoDownloadFile = runnerproto.ProtoDownloadFile

	ProtoExecResult  = runnerproto.ProtoExecResult
	ProtoFileContent = runnerproto.ProtoFileContent
	ProtoFileInfo    = runnerproto.ProtoFileInfo
	ProtoDirEntries  = runnerproto.ProtoDirEntries
	ProtoError       = runnerproto.ProtoError
	ProtoOK          = runnerproto.ProtoOK

	ProtoStdioStart = runnerproto.ProtoStdioStart
	ProtoStdioWrite = runnerproto.ProtoStdioWrite
	ProtoStdioClose = runnerproto.ProtoStdioClose
	ProtoStdioData  = runnerproto.ProtoStdioData
	ProtoStdioExit  = runnerproto.ProtoStdioExit
)

// === WebSocket Protocol Types ===

// Runner protocol type aliases re-exported from the internal runnerproto package.
// See internal/runnerclient/types.go for detailed documentation.
// RunnerMessage is a message from the runner to the server.
type RunnerMessage = runnerproto.RunnerMessage

// RegisterRequest is the initial registration message.
type RegisterRequest = runnerproto.RegisterRequest

// ExecRequest requests command execution.
type ExecRequest = runnerproto.ExecRequest

// ExecResultResponse contains command execution results.
type ExecResultResponse = runnerproto.ExecResultResponse

// ReadFileRequest requests file content.
type ReadFileRequest = runnerproto.ReadFileRequest

// FileContentResponse contains file content.
type FileContentResponse = runnerproto.FileContentResponse

// WriteFileRequest writes content to a file.
type WriteFileRequest = runnerproto.WriteFileRequest

// StatRequest requests file metadata.
type StatRequest = runnerproto.StatRequest

// StatResponse contains file metadata.
type StatResponse = runnerproto.StatResponse

// ReadDirRequest requests directory listing.
type ReadDirRequest = runnerproto.ReadDirRequest

// DirEntryResponse contains a directory entry.
type DirEntryResponse = runnerproto.DirEntryResponse

// DirEntriesResponse contains directory entries.
type DirEntriesResponse = runnerproto.DirEntriesResponse

// PathRequest requests path resolution.
type PathRequest = runnerproto.PathRequest

// DownloadFileRequest downloads a file.
type DownloadFileRequest = runnerproto.DownloadFileRequest

// DownloadFileResponse contains downloaded file content.
type DownloadFileResponse = runnerproto.DownloadFileResponse

// ErrorResponse represents an error.
type ErrorResponse = runnerproto.ErrorResponse

// StdioStartRequest starts an interactive stdio process.
type StdioStartRequest = runnerproto.StdioStartRequest

// StdioStartResponse confirms stdio process creation.
type StdioStartResponse = runnerproto.StdioStartResponse

// StdioWriteRequest sends data to a stdio process.
type StdioWriteRequest = runnerproto.StdioWriteRequest

// StdioCloseRequest closes a stdio process.
type StdioCloseRequest = runnerproto.StdioCloseRequest

// StdioDataMessage carries stdout data.
type StdioDataMessage = runnerproto.StdioDataMessage

// StdioExitMessage reports process termination.
type StdioExitMessage = runnerproto.StdioExitMessage

// ProtoErrorCodes maps protocol error codes to Go errors.
var ProtoErrorCodes = runnerproto.ProtoErrorCodes

// ProtoErrorCode converts a Go error to a protocol error code.
var ProtoErrorCode = runnerproto.ProtoErrorCode

// defaultRequestTimeout is the default timeout for non-exec operations.
const defaultRequestTimeout = runnerproto.DefaultRequestTimeout
