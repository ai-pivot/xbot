package web

import "xbot/protocol"

func (wc *WebChannel) writeCurrentWSMessage(
	client *Client,
	msg protocol.WSMessage,
	write func(protocol.WSMessage) error,
) (bool, error) {
	if client.isCLI || msg.Type != protocol.MsgTypeAskUser {
		return true, write(msg)
	}
	if msg.Progress == nil || msg.Progress.RequestID == "" || wc.callbacks.WithPendingAskUser == nil {
		return false, nil
	}

	var current protocol.WSMessage
	written := wc.callbacks.WithPendingAskUser(msg.Channel, msg.ChatID, func(pending *protocol.ProgressEvent) bool {
		if pending.RequestID != msg.Progress.RequestID {
			return false
		}
		msg.Progress = pending
		current = msg
		return true
	})
	if !written {
		return false, nil
	}
	return true, write(current)
}

// isImageExt returns true if the file extension is a common image format.
func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".tiff", ".tif":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
